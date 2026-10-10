package verify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	CommandRun  string            `json:"command_run,omitempty"`
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
	Hint             string         `json:"hint,omitempty"`
}

// Runner executes verification tasks with maximum parallelism, caching, and minimal latency.
type Runner struct {
	RootDir    string
	Quarantine *QuarantineManager
	cacheMgr   *cache.Manager
	graph      *graph.DependencyGraph
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
	// A Go module consuming pinned internal releases must build standalone: inside a
	// go.work workspace every module resolves workspace members at HEAD, ignoring its
	// replace directives into release snapshots.
	if pkg.Language == graph.LangGo && len(pkg.PinnedDependencies) > 0 {
		if _, err := os.Stat(filepath.Join(r.RootDir, "go.work")); err == nil {
			for i, c := range cmds {
				cmds[i] = "GOWORK=off " + c
			}
		}
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
		cacheKey, _ = r.cacheMgr.ComputePackageKey(pkg, nil, cmdStr, modeTag)
		if !opt.NoCache && cacheKey != "" {
			if rec, hit := r.cacheMgr.GetVerification(cacheKey); hit && rec.Status == "passed" {
				res.Status = "passed"
				res.Cached = true
				res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
				return res
			}
		}
	}

	// 3. Cache Miss: run each check in order, stopping at the first failure.
	var out []byte
	var err error
	for _, c := range cmds {
		cmd := exec.Command("sh", "-c", c)
		cmd.Dir = absPkgPath
		var o []byte
		o, err = cmd.CombinedOutput()
		out = append(out, o...)
		if err != nil {
			res.CommandRun = c
			break
		}
	}
	durationMs := float64(time.Since(start).Microseconds()) / 1000.0
	res.DurationMs = durationMs

	if err != nil {
		rawOutput := string(out)
		diagnostics := ParseDiagnostics(rawOutput)

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
	keys := map[string]string{}
	visited := map[string]bool{pkg.Name: true}
	queue := r.graph.GetUpstream(pkg.Name)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		if dep, ok := r.graph.GetPackage(name); ok {
			keys[name], _ = r.cacheMgr.ComputePackageKey(dep, nil, "", "source")
		}
		queue = append(queue, r.graph.GetUpstream(name)...)
	}
	return keys
}

// VerifyAffected runs verification in parallel for all affected packages.
func (r *Runner) VerifyAffected(g *graph.DependencyGraph, changedFiles []string, opt VerifyOptions) SuiteResult {
	start := time.Now()
	if r.graph == nil {
		r.graph = g
	}
	_ = release.EnsurePinned(r.RootDir, g)
	affected := g.ComputeAffected(changedFiles)

	if len(affected.AllAffected) == 0 {
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

	results := make([]TargetResult, len(affected.AllAffected))
	var wg sync.WaitGroup

	for i, pkgName := range affected.AllAffected {
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
		AffectedPackages: affected.AllAffected,
		Results:          results,
	}
}
