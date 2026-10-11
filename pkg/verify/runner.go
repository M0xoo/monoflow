package verify

import (
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

	// 1. Determine the checks to run (see checkCommands).
	cmds := checkCommands(pkg, opt)
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
			if rec, hit := r.cacheMgr.GetVerification(cacheKey); hit && rec.Status == "passed" {
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
	res := r.VerifyPackages(g, affected.AllAffected, opt)
	res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	return res
}

// VerifyPackages verifies the named packages in parallel and, unless Strict, classifies
// failures main already has as preexisting. Every verify mode (--affected, --package, --all)
// goes through it: --all used to report preexisting failures as new, so an agent that saw
// --affected green and --all red stopped trusting both.
func (r *Runner) VerifyPackages(g *graph.DependencyGraph, names []string, opt VerifyOptions) SuiteResult {
	start := time.Now()
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

	if !opt.Strict {
		r.classifyPreexisting(g, results, opt)
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
	cmds := checkCommands(pkg, opt)
	if len(cmds) == 0 {
		return true
	}
	key, _ := r.cacheMgr.ComputePackageKey(pkg, r.upstreamKeys(pkg), strings.Join(cmds, " && "), opt.Stage+":"+opt.Mode)
	rec, hit := r.cacheMgr.PeekVerification(key)
	return key != "" && hit && rec.Status == "passed"
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
