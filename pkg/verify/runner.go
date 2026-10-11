package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
		modeTag := opt.Stage + ":" + opt.Mode
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
		var o []byte
		o, err = r.runCheck(c, absPkgPath)
		out = append(out, o...)
		if err != nil {
			res.CommandRun = c
			res.remaining, res.dir = cmds[i+1:], absPkgPath
			break
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
	return checkCommands(pkg, opt), false
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
	var rest []string
	for _, n := range affected.AllAffected {
		if !direct[n] {
			rest = append(rest, n)
		}
	}
	res := r.VerifyPackages(g, affected.DirectlyChanged, opt)
	if res.Status == "failed" && len(rest) > 0 {
		res.Pending = rest
	} else if len(rest) > 0 {
		dep := r.VerifyPackages(g, rest, opt)
		res.Status = dep.Status
		res.TotalTargets += dep.TotalTargets
		res.PassedTargets += dep.PassedTargets
		res.FailedTargets += dep.FailedTargets
		res.CachedTargets += dep.CachedTargets
		res.AffectedPackages = append(res.AffectedPackages, dep.AffectedPackages...)
		res.Results = append(res.Results, dep.Results...)
	}
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	return res
}

// VerifyPackages verifies the named packages in parallel and, unless Strict, classifies
// failures main already has as preexisting. Every verify mode (--affected, --package, --all)
// goes through it: --all used to report preexisting failures as new, so an agent that saw
// --affected green and --all red stopped trusting both.
func (r *Runner) VerifyPackages(g *graph.DependencyGraph, names []string, opt VerifyOptions) SuiteResult {
	start := time.Now()
	if r.cacheMgr != nil {
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

	// Build what the checks need from upstream packages once, through the repo's task runner,
	// before the checks run in parallel (only for packages that will actually run).
	var toRun []*graph.PackageNode
	for _, name := range names {
		if pkg, ok := g.GetPackage(name); ok && !r.cachedPass(pkg, opt) {
			toRun = append(toRun, pkg)
		}
	}
	var prepFailures []TargetResult
	for _, c := range PrepareCommands(g, toRun) {
		if out, err := r.runCheck(c, r.RootDir); err != nil {
			prepFailures = append(prepFailures, failedResult(PrepareTarget, c, string(out)))
		}
	}

	results := make([]TargetResult, len(names))
	var wg sync.WaitGroup

	for i, pkgName := range names {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			pkg, exists := g.GetPackage(name)
			if !exists {
				results[idx] = TargetResult{
					Target: name,
					Status: "passed",
				}
				return
			}
			results[idx] = r.VerifyTarget(pkg, opt)
		}(i, pkgName)
	}

	wg.Wait()
	results = append(results, prepFailures...)

	// A failure under the load of many checks at once (test timeouts, spawned processes) is not
	// a failure of the change: re-run a few failed targets once, alone, before reporting. A
	// pass then is reported as flaky, not hidden. (trpc: openapi CLI tests timed out at 5 s
	// while 48 packages ran, blocking a correct change.)
	var failedIdx []int
	for i, res := range results {
		if res.Status == "failed" && len(res.Errors) > 0 && res.Errors[0].Code != "package_not_projected" && res.Target != PrepareTarget {
			failedIdx = append(failedIdx, i)
		}
	}
	if len(failedIdx) > 0 && len(failedIdx) <= 5 {
		for _, i := range failedIdx {
			if pkg, ok := g.GetPackage(results[i].Target); ok {
				if again := r.VerifyTarget(pkg, opt); again.Status == "passed" {
					again.Flaky = true
					results[i] = again
				}
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

// cachedPass reports whether the package's checks have a cached pass (it will not run).
func (r *Runner) cachedPass(pkg *graph.PackageNode, opt VerifyOptions) bool {
	if r.cacheMgr == nil || opt.NoCache {
		return false
	}
	cmds, unaffected := r.commandsFor(pkg, opt)
	if unaffected || len(cmds) == 0 {
		return true
	}
	key, _ := r.cacheMgr.ComputePackageKey(pkg, r.upstreamKeys(pkg), strings.Join(cmds, " && "), opt.Stage+":"+opt.Mode)
	rec, hit := r.cacheMgr.PeekVerification(key)
	return key != "" && hit && (rec.Status == "passed" || (rec.Status == "preexisting" && !opt.Strict))
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
