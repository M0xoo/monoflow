package verify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
)

// VerifyOptions configures the inner loop runner.
type VerifyOptions struct {
	TimeoutSec    int
	Concurrency   int
	Stage         string // "inner" or "outer"
	SpecificFile  string // for fine-grained TIA
}

// TargetResult holds the result of verifying a single package.
type TargetResult struct {
	Target       string            `json:"target"`
	Language     string            `json:"language"`
	Status       string            `json:"status"` // "passed" or "failed"
	DurationMs   float64           `json:"duration_ms"`
	Errors       []DiagnosticError `json:"errors,omitempty"`
	Quarantined  []string          `json:"quarantined,omitempty"`
	CommandRun   string            `json:"command_run,omitempty"`
}

// SuiteResult holds the aggregated verification results across all affected packages.
type SuiteResult struct {
	Status           string         `json:"status"` // "passed" or "failed"
	DurationMs       float64        `json:"duration_ms"`
	TotalTargets     int            `json:"total_targets"`
	PassedTargets    int            `json:"passed_targets"`
	FailedTargets    int            `json:"failed_targets"`
	AffectedPackages []string       `json:"affected_packages"`
	Results          []TargetResult `json:"results"`
}

// Runner executes verification tasks with maximum parallelism and minimal latency.
type Runner struct {
	RootDir    string
	Quarantine *QuarantineManager
}

// NewRunner creates an inner-loop runner.
func NewRunner(rootDir string) *Runner {
	return &Runner{
		RootDir:    rootDir,
		Quarantine: NewQuarantineManager(rootDir),
	}
}

// VerifyTarget executes the fastest verification commands for a single package.
func (r *Runner) VerifyTarget(pkg *graph.PackageNode, opt VerifyOptions) TargetResult {
	start := time.Now()
	res := TargetResult{
		Target:   pkg.Name,
		Language: string(pkg.Language),
		Status:   "passed",
		Errors:   make([]DiagnosticError, 0),
	}

	// Determine command: prioritize Typecheck/Lint for L1, then Test for L2
	cmdStr := pkg.TypecheckCmd
	if cmdStr == "" {
		cmdStr = pkg.TestCmd
	}
	if cmdStr == "" {
		cmdStr = pkg.LintCmd
	}

	if cmdStr == "" {
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
		return res
	}

	res.CommandRun = cmdStr
	absPkgPath := filepath.Join(r.RootDir, pkg.Path)
	if _, err := os.Stat(absPkgPath); os.IsNotExist(err) {
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
		res.Status = "passed"
		return res
	}

	// Execute command via shell
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = absPkgPath

	out, err := cmd.CombinedOutput()
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
				Message: strings.TrimSpace(StripANSI(rawOutput)),
			})
		}
	}

	return res
}

// VerifyAffected runs verification in parallel for all affected packages.
func (r *Runner) VerifyAffected(g *graph.DependencyGraph, changedFiles []string, opt VerifyOptions) SuiteResult {
	start := time.Now()
	affected := g.ComputeAffected(changedFiles)

	if len(affected.AllAffected) == 0 {
		return SuiteResult{
			Status:           "passed",
			DurationMs:       float64(time.Since(start).Microseconds()) / 1000.0,
			TotalTargets:     0,
			PassedTargets:    0,
			FailedTargets:    0,
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

	totalDur := float64(time.Since(start).Microseconds()) / 1000.0
	passedCount := 0
	failedCount := 0
	overallStatus := "passed"

	for _, res := range results {
		if res.Status == "failed" {
			failedCount++
			overallStatus = "failed"
		} else {
			passedCount++
		}
	}

	return SuiteResult{
		Status:           overallStatus,
		DurationMs:       totalDur,
		TotalTargets:     len(results),
		PassedTargets:    passedCount,
		FailedTargets:    failedCount,
		AffectedPackages: affected.AllAffected,
		Results:          results,
	}
}
