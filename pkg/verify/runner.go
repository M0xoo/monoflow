package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/release"
)

// VerifyOptions configures the inner loop runner.
type VerifyOptions struct {
	TimeoutSec   int
	Concurrency  int
	Stage        string // "inner" or "outer"
	SpecificFile string // for fine-grained TIA
	Mode         string // "fast" (typecheck/lint), "test", "lint", "all"
	NoCache      bool
	// Strict disables baseline comparison: by default a failure that also happens on the
	// merge-base (pre-existing in the repo, not caused by this change) does not block.
	Strict  bool
	BaseRef string // base for the baseline (default: origin/main, main, origin/master, master)
	// ChangedFiles (set by VerifyAffected) lets Go modules run only the packages that import
	// the change (see goScopedCommands).
	ChangedFiles []string
	// AllPackages (verify --all / --package): run every package's tests, but judge formatting
	// on the changed files only, like verify does: an agent saw verify report no preexisting
	// failure and --all report old gofmt debt, and spent five runs reconciling them.
	AllPackages bool
	// UpdateSnapshots runs JS/TS test commands with -u (vitest/jest rewrite mismatched
	// snapshots): agents otherwise rewrote inline snapshots by hand, many calls each (trpc).
	UpdateSnapshots bool
	// KeepUntracked: do not remove files the checks created (background runs: the agent may be
	// creating files at the same time).
	KeepUntracked bool
	// RetryAllFailures re-runs any failed package once, alone, before reporting it, not only
	// timing-like failures. Local CI sets it: nobody waits on it, and a flaky test there blocks
	// a correct push (trpc: a websocket keep-alive test failed under load, passed 3/3 alone).
	RetryAllFailures bool
	// LintDependents also lints packages the change only reaches through their dependencies.
	// The agent's loop leaves it off: a linter reads a package's own files, so an upstream
	// change cannot give a dependent new lint errors (TanStack Query: linting 22 dependents
	// cost ~35 s of every verify). Local CI sets it, so a push is still linted everywhere.
	LintDependents bool
	// direct (set by VerifyAffected) names the packages containing changed files.
	direct map[string]bool
	// prepared: VerifyAffected already ran the dependency builds (see VerifyPackages).
	prepared bool
	// FailFast (the agent's plain verify) returns as soon as the changed packages fail, leaving
	// their dependents pending: core tests found every failure in 6-8 s while the full verify
	// answered after 93-225 s (TanStack Query). A background run checks the dependents meanwhile.
	FailFast bool
	// BeforeDependents runs before the dependents are checked (the agent's verify waits there
	// for a background run checking them, then reuses what it cached).
	BeforeDependents func()
}

// TargetResult holds the result of verifying a single package.
type TargetResult struct {
	Target      string            `json:"target"`
	Language    string            `json:"language"`
	Status      string            `json:"status"` // "passed" or "failed"
	DurationMs  float64           `json:"duration_ms"`
	Cached      bool              `json:"cached,omitempty"`
	Errors      []DiagnosticError `json:"errors,omitempty"`
	Quarantined []string          `json:"quarantined,omitempty"`
	// Preexisting holds failures that also occur on the merge-base: reported, not blocking.
	Preexisting []DiagnosticError `json:"preexisting,omitempty"`
	// CacheKey identifies the verified inputs (content, dependencies, command, platform).
	CacheKey string `json:"cache_key,omitempty"`
	// CachedFrom is set when the cached result came from another source (e.g. an agent proof).
	CachedFrom string `json:"cached_from,omitempty"`
	CommandRun string `json:"command_run,omitempty"`
	// ErrorCount replaces Errors when DedupeErrors moved them to SuiteResult.Errors.
	ErrorCount int `json:"error_count,omitempty"`
	// Flaky: failed while many checks ran at once, passed when re-run alone.
	Flaky bool `json:"flaky,omitempty"`

	// remaining are the checks skipped after the failing one, and dir where they run: if
	// that failure turns out to be preexisting they still have to run (see classifyPreexisting).
	remaining []string
	dir       string
}

// SuiteResult holds the aggregated verification results across all affected packages.
type SuiteResult struct {
	Status           string         `json:"status"` // "passed" or "failed"
	DurationMs       float64        `json:"duration_ms"`
	TotalTargets     int            `json:"total_targets"`
	PassedTargets    int            `json:"passed_targets"`
	FailedTargets    int            `json:"failed_targets"`
	CachedTargets    int            `json:"cached_targets"`
	AffectedPackages []string       `json:"affected_packages"`
	Results          []TargetResult `json:"results"`
	// Errors lists each distinct failure once (repo-relative path) with the targets it breaks;
	// filled by DedupeErrors for compact output.
	Errors        []SharedError `json:"errors,omitempty"`
	ErrorsOmitted int           `json:"errors_omitted,omitempty"`
	Hint          string        `json:"hint,omitempty"`
	// Pending lists dependents not checked yet because a changed package failed: they run
	// once the changed packages pass.
	Pending []string `json:"pending,omitempty"`
	// Excluded counts dependents skipped by verify_exclude (default: examples/).
	Excluded      int      `json:"excluded,omitempty"`
	ExcludedNames []string `json:"-"`
}

// Runner executes verification tasks with maximum parallelism, caching, and minimal latency.
type Runner struct {
	RootDir    string
	Quarantine *QuarantineManager
	cacheMgr   *cache.Manager
	graph      *graph.DependencyGraph
	// rootRuns memoises repo-root commands (cd "$(git rev-parse --show-toplevel)" && …)
	// within one run: they do not depend on the package, so N packages run them once.
	rootMu   sync.Mutex
	rootRuns map[string]*rootRun
	// assumeEdits makes baselineSHA treat the tree as having local edits (WarmBaseline runs
	// before the agent's first edit and must prepare the baseline its later verifies use).
	assumeEdits bool
}

type rootRun struct {
	once sync.Once
	out  []byte
	err  error
}

const repoRootPrefix = `cd "$(git rev-parse --show-toplevel)" && `

func (r *Runner) runCheck(c, dir string) ([]byte, error) {
	if !strings.HasPrefix(c, repoRootPrefix) {
		cmd := exec.Command("sh", "-c", c)
		cmd.Dir = dir
		return cmd.CombinedOutput()
	}
	r.rootMu.Lock()
	if r.rootRuns == nil {
		r.rootRuns = map[string]*rootRun{}
	}
	rr, ok := r.rootRuns[c]
	if !ok {
		rr = &rootRun{}
		r.rootRuns[c] = rr
	}
	r.rootMu.Unlock()
	rr.once.Do(func() {
		cmd := exec.Command("sh", "-c", c)
		cmd.Dir = dir
		rr.out, rr.err = cmd.CombinedOutput()
	})
	return rr.out, rr.err
}

// NewRunner creates an inner-loop runner.
func NewRunner(rootDir string) *Runner {
	return &Runner{
		RootDir:    rootDir,
		Quarantine: NewQuarantineManager(rootDir),
		cacheMgr:   cache.NewManager(rootDir, cache.DefaultConfig(rootDir)),
	}
}

// SetCacheManager allows injecting a customized cache manager (e.g. for testing).
func (r *Runner) SetCacheManager(cm *cache.Manager) {
	r.cacheMgr = cm
}

// VerifyTarget executes the fastest verification commands for a single package with caching.
func (r *Runner) VerifyTarget(pkg *graph.PackageNode, opt VerifyOptions) TargetResult {
	start := time.Now()
	res := TargetResult{
		Target:   pkg.Name,
		Language: string(pkg.Language),
		Status:   "passed",
		Errors:   make([]DiagnosticError, 0),
	}

	// 1. Determine the checks to run (see commandsFor).
	cmds, unaffected := r.commandsFor(pkg, opt)
	if unaffected {
		res.CommandRun = "(none: no package of this module imports the changed packages)"
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
		return res
	}
	if len(cmds) == 0 {
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
		return res
	}
	cmdStr := strings.Join(cmds, " && ")

	res.CommandRun = cmdStr
	absPkgPath := filepath.Join(r.RootDir, pkg.Path)
	if _, err := os.Stat(absPkgPath); os.IsNotExist(err) {
		// An affected package that is not projected into this (sparse) worktree cannot be
		// vouched for; reporting it as passed let breakages reach remote CI.
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
		res.Status = "failed"
		res.Errors = append(res.Errors, DiagnosticError{
			File:    pkg.Path,
			Line:    1,
			Code:    "package_not_projected",
			Message: fmt.Sprintf("affected package %s is not present in this worktree; add it with 'monoflow subset add %s'", pkg.Name, pkg.Name),
		})
		return res
	}

	// 2. Check Verification Cache (Local and Remote)
	var cacheKey string
	if r.cacheMgr != nil {
		modeTag := keyMode(pkg, cmdStr, opt.Stage+":"+opt.Mode)
		cacheKey, _ = r.cacheMgr.ComputePackageKey(pkg, r.upstreamKeys(pkg), cmdStr, modeTag)
		if !opt.NoCache && cacheKey != "" {
			res.CacheKey = cacheKey
			rec, hit := r.cacheMgr.GetVerification(cacheKey)
			if hit && rec.Status == "preexisting" && !opt.Strict {
				// Only failures main already has, judged against the same baseline commit: they
				// stay what they were (re-running them every verify cost minutes on trpc).
				var stored struct {
					Base   string            `json:"base"`
					Errors []DiagnosticError `json:"errors"`
				}
				if json.Unmarshal([]byte(rec.Output), &stored) == nil && stored.Base != "" && stored.Base == r.baselineSHA(opt.BaseRef) {
					res.Status, res.Cached, res.Preexisting = "passed", true, stored.Errors
					res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
					return res
				}
			}
			if hit && rec.Status == "passed" {
				res.Status = "passed"
				res.Cached = true
				if strings.HasPrefix(rec.Output, "monoflow-proof:") {
					res.CachedFrom = strings.TrimSpace(strings.SplitN(rec.Output, "\n", 2)[0])
				}
				res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
				return res
			}
		}
	}

	// 3. Cache Miss: run each check in order, stopping at the first failure.
	var out []byte
	var err error
	for i, c := range cmds {
		ck := r.commandKey(pkg, c, opt)
		if ck != "" {
			if rec, hit := r.cacheMgr.PeekVerification(ck); hit && rec.Status == "passed" {
				continue
			}
		}
		var o []byte
		handled := false
		if c == pkg.TestCmd || (opt.UpdateSnapshots && c == pkg.TestCmd+" -u") {
			o, err, handled = r.runTestsIncremental(pkg, c, absPkgPath, opt)
		}
		if !handled {
			o, err = r.runCheck(c, absPkgPath)
		}
		out = append(out, o...)
		if err != nil {
			res.CommandRun = c
			res.remaining, res.dir = cmds[i+1:], absPkgPath
			break
		}
		if ck != "" {
			_ = r.cacheMgr.PutVerification(&cache.VerificationRecord{
				Key: ck, Target: pkg.Name, Language: string(pkg.Language), Status: "passed",
				CommandRun: c, Timestamp: time.Now().UTC(),
			})
		}
	}
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	res.DurationMs = durationMs

	if err != nil {
		rawOutput := string(out)
		diagnostics := ParseDiagnostics(rawOutput)
		ResolveGoTestFiles(diagnostics, absPkgPath)

		// Filter quarantined flaky tests
		activeErrors := make([]DiagnosticError, 0)
		quarantined := make([]string, 0)

		for _, diag := range diagnostics {
			ident := fmt.Sprintf("%s:%d:%s", diag.File, diag.Line, diag.Message)
			if r.Quarantine != nil && r.Quarantine.IsQuarantined(ident) {
				quarantined = append(quarantined, ident)
			} else {
				activeErrors = append(activeErrors, diag)
			}
		}

		res.Quarantined = quarantined

		if len(activeErrors) > 0 {
			res.Status = "failed"
			res.Errors = activeErrors
		} else if len(quarantined) > 0 {
			// All errors were quarantined! Pass with warnings
			res.Status = "passed"
		} else {
			// Failed without structured line diagnostics (e.g. exit 1 general error)
			res.Status = "failed"
			res.Errors = append(res.Errors, DiagnosticError{
				File:    pkg.Path,
				Line:    1,
				Code:    "execution_failure",
				Message: CompactFailure(StripANSI(rawOutput)),
			})
		}
	} else {
		// 4. Save to local & remote verification cache on success
		if r.cacheMgr != nil && !opt.NoCache && cacheKey != "" {
			_ = r.cacheMgr.PutVerification(&cache.VerificationRecord{
				Key:        cacheKey,
				Target:     pkg.Name,
				Language:   string(pkg.Language),
				Status:     "passed",
				DurationMs: durationMs,
				CommandRun: cmdStr,
				Timestamp:  time.Now().UTC(),
				Output:     string(out),
			})
			// A pass with -u is also a pass of the plain checks on the files as -u left them:
			// store it under that key too, or the next plain verify re-ran every package (trpc:
			// 11 packages, 60 s, after the agent's snapshot run).
			if opt.UpdateSnapshots && r.graph != nil {
				plain := opt
				plain.UpdateSnapshots = false
				if cmds, _ := r.commandsFor(pkg, plain); len(cmds) > 0 {
					plainCmd := strings.Join(cmds, " && ")
					if key, err := r.cacheMgr.ComputePackageKey(pkg, r.cacheMgr.UpstreamKeysFresh(r.graph, pkg), plainCmd, keyMode(pkg, plainCmd, opt.Stage+":"+opt.Mode)); err == nil && key != "" {
						_ = r.cacheMgr.PutVerification(&cache.VerificationRecord{
							Key: key, Target: pkg.Name, Language: string(pkg.Language), Status: "passed",
							DurationMs: durationMs, CommandRun: plainCmd, Timestamp: time.Now().UTC(), Output: string(out),
						})
					}
				}
			}
		}
	}

	return res
}

// commandsFor is checkCommands narrowed to what the change can affect (Go: only packages
// importing the changed packages); unaffected=true when nothing in the package can be.
func (r *Runner) commandsFor(pkg *graph.PackageNode, opt VerifyOptions) (cmds []string, unaffected bool) {
	if len(opt.ChangedFiles) > 0 && (opt.Mode == "" || opt.Mode == "all") {
		if opt.AllPackages {
			if c, ok := goFormatScopedCommands(r.RootDir, pkg, opt.ChangedFiles); ok {
				return c, false
			}
		} else if c, skip, ok := goScopedCommands(r.RootDir, pkg, opt.ChangedFiles); ok {
			return c, skip
		}
	}
	cmds = checkCommands(pkg, opt)
	if !opt.LintDependents && !opt.AllPackages && opt.direct != nil && !opt.direct[pkg.Name] && pkg.LintCmd != "" && pkg.LintCmd != pkg.TypecheckCmd && pkg.LintCmd != pkg.TestCmd {
		kept := cmds[:0:0]
		for _, c := range cmds {
			if c != pkg.LintCmd {
				kept = append(kept, c)
			}
		}
		cmds = kept
	}
	if opt.UpdateSnapshots && (pkg.Language == graph.LangJavaScript || pkg.Language == graph.LangTypeScript) {
		for i, c := range cmds {
			if c == pkg.TestCmd {
				cmds[i] = c + " -u"
			}
		}
	}
	return cmds, false
}

// keyMode is the cache key mode for commands: exact file contents when they lint (formatting
// aliases apply to tests and type checks only; a formatter can fix what a linter reports).
func keyMode(pkg *graph.PackageNode, cmdStr, mode string) string {
	if pkg.LintCmd != "" && strings.Contains(cmdStr, pkg.LintCmd) {
		return mode + cache.ExactMode
	}
	return mode
}

// commandKey identifies one check command's pass on the package's current inputs, so a
// package failing on one check does not re-run the others that passed (TanStack Query: every
// verify re-ran typecheck and lint of the packages whose tests the agent was fixing), and local
// CI, running the same commands, reuses them. "" when there is no cache or -u rewrites files.
func (r *Runner) commandKey(pkg *graph.PackageNode, c string, opt VerifyOptions) string {
	if r.cacheMgr == nil || opt.NoCache || opt.UpdateSnapshots {
		return ""
	}
	key, err := r.cacheMgr.ComputePackageKey(pkg, r.upstreamKeys(pkg), c, keyMode(pkg, c, "command"))
	if err != nil {
		return ""
	}
	return key
}

// checkCommands returns the commands for the requested checks, in run order.
// The default (and "all") runs typecheck, lint and tests so the inner loop catches
// everything remote CI would; --typecheck/--lint/--test narrow it to a single check.
func checkCommands(pkg *graph.PackageNode, opt VerifyOptions) []string {
	first := func(cands ...string) []string {
		for _, c := range cands {
			if c != "" {
				return []string{c}
			}
		}
		return nil
	}
	switch {
	case opt.Mode == "test":
		return first(pkg.TestCmd, pkg.TypecheckCmd, pkg.LintCmd)
	case opt.Mode == "lint":
		return first(pkg.LintCmd, pkg.TypecheckCmd)
	case opt.Mode == "fast":
		return first(pkg.TypecheckCmd, pkg.LintCmd, pkg.TestCmd)
	}
	var cmds []string
	seen := map[string]bool{}
	for _, c := range []string{pkg.TypecheckCmd, pkg.LintCmd, pkg.TestCmd} {
		if c != "" && !seen[c] {
			seen[c] = true
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// SetGraph lets cache keys incorporate upstream dependency sources.
func (r *Runner) SetGraph(g *graph.DependencyGraph) {
	r.graph = g
}

// upstreamKeys hashes the sources of every transitive upstream dependency so a change
// in a dependency invalidates cached results of its dependents.
func (r *Runner) upstreamKeys(pkg *graph.PackageNode) map[string]string {
	if r.graph == nil || r.cacheMgr == nil {
		return nil
	}
	return r.cacheMgr.UpstreamKeys(r.graph, pkg)
}

// VerifyAffected runs verification in parallel for all affected packages.
func (r *Runner) VerifyAffected(g *graph.DependencyGraph, changedFiles []string, opt VerifyOptions) SuiteResult {
	start := time.Now()
	if r.graph == nil {
		r.graph = g
	}
	_ = release.EnsurePinned(r.RootDir, g)
	affected := g.ComputeAffected(changedFiles)
	opt.ChangedFiles = changedFiles
	// Changed packages first: while they fail, checking their dependents only slows the fix loop
	// (each edit re-runs them all: 1-2 min on trpc, where an agent iterating on one package wants
	// seconds). Once they pass, the dependents run, so a pass still covers what CI runs.
	direct := map[string]bool{}
	for _, n := range affected.DirectlyChanged {
		direct[n] = true
	}
	opt.direct = direct
	var rest, excludedNames []string
	excluded := 0
	for _, n := range affected.AllAffected {
		if direct[n] {
			continue
		}
		if p, ok := g.GetPackage(n); ok && g.ExcludedFromVerify(p) {
			excluded++ // verify_exclude (default: examples/), as the repo's CI does
			excludedNames = append(excludedNames, n)
			continue
		}
		rest = append(rest, n)
	}
	// Dependency builds for the dependents run while the changed packages are checked (TanStack
	// Query: 24 s of nx builds used to wait for the 18 s of checks before them). The changed
	// packages' own builds run first: two builds of one package at once would race on its output.
	if r.graph == nil {
		r.graph = g
	}
	graph.WaitForDeps(r.RootDir, 10*time.Minute)
	directPrep := r.prepare(g, affected.DirectlyChanged, opt)
	var restPrep []TargetResult
	prepDone := make(chan struct{})
	if opt.FailFast {
		close(prepDone) // built only once the changed packages pass (below)
	} else {
		go func(opt VerifyOptions) {
			defer close(prepDone)
			restPrep = r.prepare(g, rest, opt)
		}(opt)
	}
	opt.prepared = true
	res := r.VerifyPackages(g, affected.DirectlyChanged, opt)
	<-prepDone
	if len(directPrep) > 0 {
		res.Results = append(res.Results, directPrep...)
		res.Status = "failed"
	}
	// Hold the dependents back only while the changed packages are broken (type, build or lint
	// errors: the dependents would fail on the same thing). When all that fails is test
	// assertions, the change itself compiles, and its dependents' tests are what the agent must
	// update next: run them now, so one verify shows every test to fix (trpc: staging cost an
	// extra 45-90 s verify round per task).
	if res.Status == "failed" && len(rest) > 0 && (opt.FailFast || !onlyTestFailures(res)) {
		res.Pending = rest
	} else if len(rest) > 0 && opt.FailFast && !r.allCached(g, rest, opt) {
		// The changed packages pass; their dependents are still to check (minutes on TanStack
		// Query). Answer now: a verify blocking past the harness's 120 s tool limit was moved to
		// the background, and the agent left docs and changesets for after it (~100 s).
		res.Pending = rest
	} else if len(rest) > 0 {
		if opt.BeforeDependents != nil {
			opt.BeforeDependents()
		}
		if opt.FailFast {
			restPrep = r.prepare(g, rest, opt)
		}
		failedFirst := res.Status == "failed"
		dep := r.VerifyPackages(g, rest, opt)
		if len(restPrep) > 0 {
			dep.Results = append(dep.Results, restPrep...)
			dep.Status = "failed"
			dep.TotalTargets += len(restPrep)
			dep.FailedTargets += len(restPrep)
		}
		res.Status = dep.Status
		if failedFirst {
			res.Status = "failed"
		}
		res.TotalTargets += dep.TotalTargets
		res.PassedTargets += dep.PassedTargets
		res.FailedTargets += dep.FailedTargets
		res.CachedTargets += dep.CachedTargets
		res.AffectedPackages = append(res.AffectedPackages, dep.AffectedPackages...)
		res.Results = append(res.Results, dep.Results...)
	}
	res.Excluded, res.ExcludedNames = excluded, excludedNames
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	return res
}

// onlyTestFailures reports whether every failed package failed on test assertions only.
func onlyTestFailures(res SuiteResult) bool {
	any := false
	for _, t := range res.Results {
		if t.Status != "failed" {
			continue
		}
		if len(t.Errors) == 0 {
			return false
		}
		for _, e := range t.Errors {
			if e.Code != "test-failure" {
				return false
			}
		}
		any = true
	}
	return any
}

// VerifyPackages verifies the named packages in parallel and, unless Strict, classifies
// failures main already has as preexisting. Every verify mode (--affected, --package, --all)
// goes through it: --all used to report preexisting failures as new, so an agent that saw
// --affected green and --all red stopped trusting both.
func (r *Runner) VerifyPackages(g *graph.DependencyGraph, names []string, opt VerifyOptions) SuiteResult {
	start := time.Now()
	// No key memo when -u rewrites snapshots mid-run: keys computed before the rewrite would
	// file later packages' passes under content that no longer exists.
	if r.cacheMgr != nil && !opt.UpdateSnapshots {
		r.cacheMgr.BeginRun()
		defer r.cacheMgr.EndRun()
	}
	if r.graph == nil {
		r.graph = g
	}
	if len(names) == 0 {
		return SuiteResult{
			Status:           "passed",
			DurationMs:       float64(time.Since(start).Microseconds()) / 1000.0,
			TotalTargets:     0,
			PassedTargets:    0,
			FailedTargets:    0,
			CachedTargets:    0,
			AffectedPackages: []string{},
			Results:          []TargetResult{},
		}
	}

	// A worktree's dependencies may still be installing in the background (pnpm, see
	// graph.PrepareNodeModules): checks need them.
	graph.WaitForDeps(r.RootDir, 10*time.Minute)

	// Checks must not change the tree the agent commits: files they create (Playwright's
	// test-results/, coverage) are removed afterwards. trpc: ship committed two of them.
	// A background (speculative) run leaves them: the agent may be creating files meanwhile.
	if !opt.UpdateSnapshots && !opt.KeepUntracked { // new snapshot files are the point of -u
		before := untrackedFiles(r.RootDir)
		defer removeNewUntracked(r.RootDir, before)
	}

	var prepFailures []TargetResult
	if !opt.prepared {
		prepFailures = r.prepare(g, names, opt)
	}

	results := make([]TargetResult, len(names))
	var wg sync.WaitGroup

	// Bounded: every target at once (48 on trpc, each starting vitest/tsc with its own workers)
	// oversubscribed the machine and made timing-sensitive tests fail (websocket reconnects).
	limit := opt.Concurrency
	if n, err := strconv.Atoi(os.Getenv("MONOFLOW_VERIFY_CONCURRENCY")); err == nil && n > 0 && limit <= 0 {
		limit = n
	}
	if limit <= 0 {
		// One package per CPU: measured cold, 8 at once on 8 cores took 118 s on TanStack Query
		// where 4 took 166 s (trpc: 60 s, 70 s), same verdicts. Load-induced test failures are
		// handled by re-running the failed test files alone.
		limit = min(max(runtime.NumCPU(), 2), 8)
	}
	// Slowest first: the run ends when its last package does (TanStack Query: vue-query's 29 s
	// suite started last and set the end of the run).
	times := loadDurations(r.RootDir)
	order := slowestFirst(r.RootDir, g, names, times)
	next := make(chan int, len(order))
	for _, i := range order {
		next <- i
	}
	close(next)
	var timesMu sync.Mutex
	for w := 0; w < min(limit, len(names)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range next {
				name := names[idx]
				pkg, exists := g.GetPackage(name)
				if !exists {
					results[idx] = TargetResult{Target: name, Status: "passed"}
					continue
				}
				results[idx] = r.VerifyTarget(pkg, opt)
				if !results[idx].Cached && results[idx].DurationMs > 0 {
					timesMu.Lock()
					times[name] = results[idx].DurationMs
					timesMu.Unlock()
				}
			}
		}()
	}

	wg.Wait()
	saveDurations(r.RootDir, times)
	results = append(results, prepFailures...)

	// A failure under the load of many checks at once (test timeouts, spawned processes) is not
	// a failure of the change: re-run a few failed targets once, alone, before reporting. A
	// pass then is reported as flaky, not hidden. (trpc: openapi CLI tests timed out at 5 s
	// while 48 packages ran, blocking a correct change.)
	var failedIdx []int
	for i, res := range results {
		if res.Status == "failed" && len(res.Errors) > 0 && res.Errors[0].Code != "package_not_projected" && res.Target != PrepareTarget && (opt.RetryAllFailures || timingFailure(res.Errors)) {
			// Failed test files alone are re-run below, in seconds; the whole package again
			// (TanStack Query: 4 packages, typecheck, lint and tests, 49 s) adds nothing.
			if pkg, ok := g.GetPackage(res.Target); ok {
				if _, ok := r.failedTestFilesCmd(&results[i], pkg.Path); ok {
					continue
				}
			}
			failedIdx = append(failedIdx, i)
		}
	}
	if len(failedIdx) > 0 && (len(failedIdx) <= 5 || opt.RetryAllFailures) {
		for _, i := range failedIdx {
			if pkg, ok := g.GetPackage(results[i].Target); ok {
				if again := r.VerifyTarget(pkg, opt); again.Status == "passed" {
					again.Flaky = true
					results[i] = again
				}
			}
		}
	}

	// A test that fails while dozens of checks run at once but passes alone (trpc: a websocket
	// keep-alive assertion) blocked a correct change and cost the agent two more full verifies.
	// Re-running only the failed test files is seconds, so every new test failure gets it once.
	for i := range results {
		pkg, ok := g.GetPackage(results[i].Target)
		if !ok || results[i].Status != "failed" || results[i].Flaky {
			continue
		}
		// Judged per failure: a pre-existing failure in the same files (trpc: a memory test that
		// fails on main) must not make a flaky one look persistent and block the push.
		if remaining, ran := r.retryFailedTestFiles(&results[i], pkg.Path); ran {
			if len(remaining) == 0 {
				results[i].Status, results[i].Flaky = "passed", true
				results[i].Errors, results[i].ErrorCount = []DiagnosticError{}, 0
			} else {
				results[i].Errors = remaining
			}
		}
	}

	if !opt.Strict {
		r.classifyPreexisting(g, results, opt)
		r.cachePreexisting(results, opt)
	}

	totalDur := float64(time.Since(start).Microseconds()) / 1000.0
	passedCount := 0
	failedCount := 0
	cachedCount := 0
	overallStatus := "passed"

	for _, res := range results {
		if res.Status == "failed" {
			failedCount++
			overallStatus = "failed"
		} else {
			passedCount++
			if res.Cached {
				cachedCount++
			}
		}
	}

	return SuiteResult{
		Status:           overallStatus,
		DurationMs:       totalDur,
		TotalTargets:     len(results),
		PassedTargets:    passedCount,
		FailedTargets:    failedCount,
		CachedTargets:    cachedCount,
		AffectedPackages: names,
		Results:          results,
	}
}

// prepare builds what the named packages' checks need from upstream packages, once, through the
// repo's task runner (only for packages that will actually run), and returns the failed builds.
func (r *Runner) prepare(g *graph.DependencyGraph, names []string, opt VerifyOptions) []TargetResult {
	var toRun []*graph.PackageNode
	for _, name := range names {
		if pkg, ok := g.GetPackage(name); ok && !r.cachedPass(pkg, opt) {
			toRun = append(toRun, pkg)
		}
	}
	var failures []TargetResult
	for _, c := range PrepareCommands(g, toRun) {
		if out, err := r.runCheck(c, r.RootDir); err != nil {
			failures = append(failures, failedResult(PrepareTarget, c, string(out)))
		}
	}
	return failures
}

// durationsFile holds each package's last check time, shared by the repository's worktrees.
func durationsFile(root string) string {
	return filepath.Join(cache.SharedCacheDir(root), "durations.json")
}

func loadDurations(root string) map[string]float64 {
	times := map[string]float64{}
	if data, err := os.ReadFile(durationsFile(root)); err == nil {
		_ = json.Unmarshal(data, &times)
	}
	return times
}

func saveDurations(root string, times map[string]float64) {
	if data, err := json.Marshal(times); err == nil {
		p := durationsFile(root)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		tmp := fmt.Sprintf("%s.%d", p, os.Getpid())
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, p)
		}
	}
}

// slowestFirst orders the indexes of names by each package's last check time, longest first;
// a package never timed ranks by its number of files (a larger package has more to check).
func slowestFirst(root string, g *graph.DependencyGraph, names []string, times map[string]float64) []int {
	size := map[string]int{}
	known := true
	for _, n := range names {
		if times[n] == 0 {
			known = false
		}
	}
	if !known {
		if out, err := exec.Command("git", "-C", root, "ls-files").Output(); err == nil {
			paths := map[string]string{}
			for _, n := range names {
				if p, ok := g.GetPackage(n); ok && p.Path != "" && p.Path != "." {
					paths[filepath.ToSlash(p.Path)+"/"] = n
				}
			}
			for _, f := range strings.Split(string(out), "\n") {
				for dir := path.Dir(f); dir != "." && dir != "/"; dir = path.Dir(dir) {
					if n, ok := paths[dir+"/"]; ok {
						size[n]++
						break
					}
				}
			}
		}
	}
	order := make([]int, len(names))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		na, nb := names[order[a]], names[order[b]]
		ta, tb := times[na], times[nb]
		if ta != tb && ta > 0 && tb > 0 {
			return ta > tb
		}
		if (ta > 0) != (tb > 0) {
			return ta == 0 // never timed first: it may be the slowest
		}
		return size[na] > size[nb]
	})
	return order
}

// PrepareTarget names the result of a failed dependency build (PackageNode.PrepareCmd).
const PrepareTarget = "(dependency build)"

// PrepareCommands merges the PrepareCmd of the given packages into one runner invocation per
// distinct command, naming every upstream package they need built.
func PrepareCommands(g *graph.DependencyGraph, pkgs []*graph.PackageNode) []string {
	deps := map[string]map[string]bool{}
	var order []string
	for _, p := range pkgs {
		if p.PrepareCmd == "" {
			continue
		}
		if deps[p.PrepareCmd] == nil {
			deps[p.PrepareCmd] = map[string]bool{}
			order = append(order, p.PrepareCmd)
		}
		for _, d := range g.GetTransitiveUpstream(p.Name) {
			deps[p.PrepareCmd][d] = true
		}
	}
	var cmds []string
	for _, c := range order {
		var names []string
		for d := range deps[c] {
			names = append(names, d)
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		if strings.Contains(c, " nx run-many") {
			cmds = append(cmds, c+" --projects="+strings.Join(names, ","))
		} else {
			cmds = append(cmds, c+" --filter="+strings.Join(names, " --filter="))
		}
	}
	return cmds
}

// allCached reports whether every named package has a cached pass (checking them is instant).
func (r *Runner) allCached(g *graph.DependencyGraph, names []string, opt VerifyOptions) bool {
	for _, n := range names {
		if pkg, ok := g.GetPackage(n); ok && !g.ExcludedFromVerify(pkg) && !r.cachedPass(pkg, opt) {
			return false
		}
	}
	return true
}

// cachedPass reports whether the package's checks have a cached pass (it will not run).
func (r *Runner) cachedPass(pkg *graph.PackageNode, opt VerifyOptions) bool {
	if r.cacheMgr == nil || opt.NoCache {
		return false
	}
	cmds, unaffected := r.commandsFor(pkg, opt)
	if unaffected || len(cmds) == 0 {
		return true
	}
	key, _ := r.cacheMgr.ComputePackageKey(pkg, r.upstreamKeys(pkg), strings.Join(cmds, " && "), keyMode(pkg, strings.Join(cmds, " && "), opt.Stage+":"+opt.Mode))
	if rec, hit := r.cacheMgr.PeekVerification(key); key != "" && hit && (rec.Status == "passed" || (rec.Status == "preexisting" && !opt.Strict)) {
		return true
	}
	for _, c := range cmds {
		ck := r.commandKey(pkg, c, opt)
		if ck == "" {
			return false
		}
		if rec, hit := r.cacheMgr.PeekVerification(ck); !hit || rec.Status != "passed" {
			return false
		}
	}
	return true
}

// failedResult turns a failed command's output into a result with diagnostics.
func failedResult(target, cmd, out string) TargetResult {
	errs := ParseDiagnostics(out)
	if len(errs) == 0 {
		errs = []DiagnosticError{{File: ".", Line: 1, Code: "execution_failure", Message: CompactFailure(StripANSI(out))}}
	}
	return TargetResult{Target: target, Status: "failed", CommandRun: cmd, Errors: errs}
}

// PassedKeys returns the cache keys of passed (non-imported) results, i.e. what this
// machine verified and can publish as proofs.
func PassedKeys(results []TargetResult) []string {
	var keys []string
	for _, r := range results {
		if r.Status == "passed" && r.CacheKey != "" && r.CachedFrom == "" {
			keys = append(keys, r.CacheKey)
		}
	}
	return keys
}

// cachePreexisting stores results whose only failures main already has, with the baseline
// commit they were judged against, so the next verify does not re-run them.
func (r *Runner) cachePreexisting(results []TargetResult, opt VerifyOptions) {
	if r.cacheMgr == nil || opt.NoCache {
		return
	}
	base := ""
	for _, res := range results {
		if res.Status != "passed" || res.Cached || res.CacheKey == "" || len(res.Preexisting) == 0 || len(res.Errors) > 0 {
			continue
		}
		if base == "" {
			if base = r.baselineSHA(opt.BaseRef); base == "" {
				return
			}
		}
		data, _ := json.Marshal(map[string]any{"base": base, "errors": res.Preexisting})
		_ = r.cacheMgr.PutVerification(&cache.VerificationRecord{
			Key: res.CacheKey, Target: res.Target, Language: res.Language, Status: "preexisting",
			CommandRun: res.CommandRun, Timestamp: time.Now().UTC(), Output: string(data),
		})
	}
}

// untrackedFiles lists files git would add (untracked, not ignored), or nil outside git.
func untrackedFiles(dir string) map[string]bool {
	out, err := exec.Command("git", "-C", dir, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil
	}
	set := map[string]bool{}
	for _, f := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if f != "" {
			set[f] = true
		}
	}
	return set
}

// removeNewUntracked deletes untracked files that appeared since before (and the directories
// they leave empty).
func removeNewUntracked(dir string, before map[string]bool) {
	if before == nil {
		return
	}
	for f := range untrackedFiles(dir) {
		if before[f] || strings.HasPrefix(f, ".agents/") {
			continue
		}
		p := filepath.Join(dir, f)
		_ = os.Remove(p)
		for d := filepath.Dir(p); d != dir && strings.HasPrefix(d, dir); d = filepath.Dir(d) {
			if os.Remove(d) != nil { // stops at the first non-empty directory
				break
			}
		}
	}
}

// testRunnerCall finds the repo's own root-level vitest/jest invocation in a check command.
var testRunnerCall = regexp.MustCompile(`cd "\$\(git rev-parse --show-toplevel\)" && (\S+(?: exec| x)?) (vitest run|jest)\b`)

// jsTestFile matches the test files vitest/jest report failures in.
var jsTestFile = regexp.MustCompile(`\.(test|spec)\.[cm]?[jt]sx?$`)

// failedTestFilesCmd returns the command running only the test files a vitest/jest check
// reported failing (repo-relative paths, run from the repo root), when every error is in such
// a file (a type or lint error is not) and there are at most 10. Paths in errors may be repo-
// or package-relative.
func (r *Runner) failedTestFilesCmd(res *TargetResult, pkgPath string) (string, bool) {
	m := testRunnerCall.FindStringSubmatch(res.CommandRun)
	if m == nil || len(res.Errors) == 0 {
		return "", false
	}
	seen := map[string]bool{}
	var files []string
	for _, e := range res.Errors {
		if !jsTestFile.MatchString(e.File) {
			return "", false
		}
		f := filepath.ToSlash(filepath.Clean(e.File))
		if _, err := os.Stat(filepath.Join(r.RootDir, f)); err != nil {
			f = filepath.ToSlash(filepath.Join(pkgPath, f))
			if _, err := os.Stat(filepath.Join(r.RootDir, f)); err != nil {
				return "", false
			}
		}
		if !seen[f] {
			seen[f] = true
			files = append(files, "'"+strings.ReplaceAll(f, "'", `'\''`)+"'")
		}
	}
	if len(files) > 10 {
		return "", false
	}
	return repoRootPrefix + m[1] + " " + m[2] + " " + strings.Join(files, " "), true
}

// retryFailedTestFiles re-runs, once and alone, the failed test files of a vitest/jest check
// and returns the failures that happen again (a failure whose test passes this time is
// flaky and dropped; one the re-run cannot account for is kept). ran=false: not re-runnable.
func (r *Runner) retryFailedTestFiles(res *TargetResult, pkgPath string) (remaining []DiagnosticError, ran bool) {
	c, ok := r.failedTestFilesCmd(res, pkgPath)
	if !ok {
		return nil, false
	}
	cmd := exec.Command("sh", "-c", c)
	cmd.Dir = r.RootDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil, true
	}
	again := map[string]bool{}
	for _, e := range ParseDiagnostics(StripANSI(string(out))) {
		if name := testName(e.Message); name != "" {
			again[name] = true
		}
	}
	for _, e := range res.Errors {
		if name := testName(e.Message); name == "" || again[name] {
			remaining = append(remaining, e)
		}
	}
	return remaining, true
}

// testName is the "[suite > test]" prefix the parsers put on a test failure ("" if none).
func testName(msg string) string {
	if !strings.HasPrefix(msg, "[") {
		return ""
	}
	if i := strings.Index(msg, "]"); i > 0 {
		return msg[1:i]
	}
	return ""
}

// timingFlake matches failures that load can cause (a deterministic assertion or snapshot
// mismatch is never retried: re-running trpc's failing packages cost ~60 s per verify).
var timingFlake = regexp.MustCompile(`(?i)timed? ?out|timeout|ETIMEDOUT|ECONNRESET|ECONNREFUSED|EADDRINUSE|socket hang up|deadline exceeded|reconnect`)

func timingFailure(errs []DiagnosticError) bool {
	for _, e := range errs {
		// The failure, not the test's name: TanStack Query's "refetchOnReconnect" tests made
		// every assertion failure in them look like a timing flake.
		msg := e.Message
		if name := testName(msg); name != "" {
			msg = msg[len(name)+2:]
		}
		if timingFlake.MatchString(msg) {
			return true
		}
	}
	return false
}
