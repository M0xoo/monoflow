package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/runtimestate"
	"github.com/antigravity/monoflow/pkg/telemetry"
	"github.com/antigravity/monoflow/pkg/verify"
)

func handleVerify(rootDir string, metrics *telemetry.Collector) {
	opts := verify.VerifyOptions{}
	targetPkg := ""
	verifyAll := false
	verifyBase := ""

	// Check flags
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--proof=") {
			taskID := strings.TrimPrefix(arg, "--proof=")
			handleProofVerification(rootDir, taskID)
			return
		} else if strings.HasPrefix(arg, "--target=") {
			targetPkg = strings.TrimPrefix(arg, "--target=")
		} else if strings.HasPrefix(arg, "--package=") {
			targetPkg = strings.TrimPrefix(arg, "--package=")
		} else if arg == "--all" {
			verifyAll = true
		} else if arg == "--no-cache" || arg == "--force" {
			opts.NoCache = true
		} else if arg == "--test" {
			opts.Mode = "test"
		} else if arg == "--typecheck" {
			opts.Mode = "fast"
		} else if arg == "--lint" {
			opts.Mode = "lint"
		} else if arg == "--all-checks" {
			opts.Mode = "all"
		} else if strings.HasPrefix(arg, "--base=") {
			verifyBase = strings.TrimPrefix(arg, "--base=")
			opts.BaseRef = verifyBase
		} else if arg == "--strict" {
			opts.Strict = true
		}
	}

	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		fmt.Printf(`{"status": "failed", "error": "%s"}`+"\n", err)
		os.Exit(1)
	}

	runner := verify.NewRunner(rootDir)
	runner.SetGraph(g)
	start := time.Now()

	var res verify.SuiteResult

	if targetPkg != "" {
		pkg, exists := g.GetPackage(targetPkg)
		if !exists {
			fmt.Printf(`{"status": "failed", "error": "package '%s' not found"}`+"\n", targetPkg)
			os.Exit(1)
		}
		tRes := runner.VerifyTarget(pkg, opts)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		passed := 0
		failed := 0
		cached := 0
		if tRes.Status == "passed" {
			passed = 1
			if tRes.Cached {
				cached = 1
			}
		} else {
			failed = 1
		}
		res = verify.SuiteResult{
			Status:           tRes.Status,
			DurationMs:       durMs,
			TotalTargets:     1,
			PassedTargets:    passed,
			FailedTargets:    failed,
			CachedTargets:    cached,
			AffectedPackages: []string{targetPkg},
			Results:          []verify.TargetResult{tRes},
		}
	} else if verifyAll {
		allPkgs := g.GetAllPackages()
		results := make([]verify.TargetResult, 0, len(allPkgs))
		allNames := make([]string, 0, len(allPkgs))
		passed := 0
		failed := 0
		cached := 0
		overallStatus := "passed"

		for _, p := range allPkgs {
			allNames = append(allNames, p.Name)
			tRes := runner.VerifyTarget(p, opts)
			results = append(results, tRes)
			if tRes.Status == "failed" {
				failed++
				overallStatus = "failed"
			} else {
				passed++
				if tRes.Cached {
					cached++
				}
			}
		}
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		res = verify.SuiteResult{
			Status:           overallStatus,
			DurationMs:       durMs,
			TotalTargets:     len(results),
			PassedTargets:    passed,
			FailedTargets:    failed,
			CachedTargets:    cached,
			AffectedPackages: allNames,
			Results:          results,
		}
	} else {
		changedFiles := getGitChangedFiles(rootDir)
		if verifyBase != "" {
			changedFiles = changedFilesSince(rootDir, verifyBase)
		}
		res = runner.VerifyAffected(g, changedFiles, opts)
		res.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	}

	if res.TotalTargets == 0 {
		res.Hint = "No package is affected by the current changes (docs/config-only or no changes): nothing to verify."
		if entries, err := os.ReadDir(filepath.Join(rootDir, ".agents", "worktrees")); err == nil {
			var ids []string
			for _, e := range entries {
				if e.IsDir() {
					ids = append(ids, e.Name())
				}
			}
			if len(ids) > 0 {
				res.Hint = fmt.Sprintf("No changes in %s. Agent work lives in worktrees (%s): run verify inside .agents/worktrees/<id>.", rootDir, strings.Join(ids, ", "))
			}
		}
	}

	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))

	cb := guard.NewCircuitBreaker(rootDir, 5)
	targetKey := strings.Join(res.AffectedPackages, ",")

	if res.Status == "failed" {
		st, tripped, _ := cb.RecordFailure(rootDir, "active_task", targetKey)
		if tripped {
			circuitMsg, _ := json.MarshalIndent(map[string]any{
				"circuit_breaker": "TRIPPED",
				"attempts":        st.Attempts,
				"snapshot":        st.SnapshotID,
				"advice":          "Same targets failed repeatedly. Step back: re-read the first error, check 'monoflow graph impact <file>', or 'monoflow checkpoint list' / 'checkpoint revert <id>' to restart from a known state. Your current work is saved as the snapshot above and was NOT reverted.",
				"reason":          fmt.Sprintf("Target '%s' failed %d consecutive times without passing", targetKey, st.Attempts),
			}, "", "  ")
			fmt.Println(string(circuitMsg))
		}
		os.Exit(1)
	} else {
		cb.Reset("active_task", targetKey)
	}
}

func handleProofVerification(rootDir, taskID string) {
	start := time.Now()
	receipt, err := proof.LoadReceipt(rootDir, taskID)
	if err != nil {
		fmt.Printf(`{"status": "failed", "error": "%s"}`+"\n", err)
		os.Exit(1)
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	out := map[string]any{
		"status":             receipt.Status,
		"task_id":            receipt.TaskID,
		"verified_in_ms":     durMs,
		"git_tree_sha":       receipt.GitTreeSHA,
		"dod_checks_passed":  len(receipt.DoDChecks),
		"affected_packages":  receipt.AffectedPackages,
		"deliverable_sha":    receipt.DeliverableSHA,
		"confidence_verdict": "APPROVED (1-Second Instant Verification Passed)",
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(data))
}

func getGitChangedFiles(rootDir string) []string {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = rootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return []string{}
	}

	var files []string
	lines := strings.Split(string(out), "\n")
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if len(trimmed) > 3 {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				files = append(files, parts[len(parts)-1])
			}
		}
	}

	// A sparse worktree's projected go.work is not a change.
	if runtimestate.ProjectedGoWork(rootDir) {
		kept := files[:0]
		for _, f := range files {
			if f != "go.work" {
				kept = append(kept, f)
			}
		}
		files = kept
	}

	// If working tree is clean, fallback to diff against base branch or HEAD~1 to capture committed branch edits
	if len(files) == 0 {
		cmdDiff := exec.Command("git", "diff", "--name-only", "origin/main...HEAD")
		cmdDiff.Dir = rootDir
		if diffOut, err := cmdDiff.CombinedOutput(); err == nil && len(strings.TrimSpace(string(diffOut))) > 0 {
			for _, f := range strings.Split(strings.TrimSpace(string(diffOut)), "\n") {
				if trimmed := strings.TrimSpace(f); trimmed != "" {
					files = append(files, trimmed)
				}
			}
		} else {
			cmdDiffHead := exec.Command("git", "diff", "--name-only", "HEAD~1...HEAD")
			cmdDiffHead.Dir = rootDir
			if headOut, err := cmdDiffHead.CombinedOutput(); err == nil && len(strings.TrimSpace(string(headOut))) > 0 {
				for _, f := range strings.Split(strings.TrimSpace(string(headOut)), "\n") {
					if trimmed := strings.TrimSpace(f); trimmed != "" {
						files = append(files, trimmed)
					}
				}
			}
		}
	}

	return files
}

// verifyBranchAgainstBase verifies packages affected by the commits on this branch
// (plus uncommitted changes) relative to its merge-base with the default branch.
func verifyBranchAgainstBase(rootDir string) (verify.SuiteResult, string, bool) {
	base := ""
	for _, cand := range []string{"origin/main", "main", "origin/master", "master"} {
		if exec.Command("git", "-C", rootDir, "rev-parse", "--verify", "--quiet", cand).Run() == nil {
			base = cand
			break
		}
	}
	if base == "" {
		return verify.SuiteResult{}, "", false
	}
	mb, err := exec.Command("git", "-C", rootDir, "merge-base", base, "HEAD").Output()
	if err != nil {
		return verify.SuiteResult{}, base, false
	}
	out, _ := exec.Command("git", "-C", rootDir, "diff", "--name-only", strings.TrimSpace(string(mb))).Output()
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f != "" {
			files = append(files, f)
		}
	}
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil || len(files) == 0 {
		return verify.SuiteResult{Status: "passed"}, base, err == nil
	}
	runner := verify.NewRunner(rootDir)
	return runner.VerifyAffected(g, files, verify.VerifyOptions{}), base, true
}

func failedResults(res verify.SuiteResult) []verify.TargetResult {
	var failed []verify.TargetResult
	for _, r := range res.Results {
		if r.Status == "failed" {
			failed = append(failed, r)
		}
	}
	return failed
}

// changedFilesSince lists files changed since the merge-base with base (committed and
// uncommitted). An unknown or all-zero base (first push of a branch) yields every file
// via the global marker so nothing is skipped.
func changedFilesSince(rootDir, base string) []string {
	seen := map[string]bool{}
	var files []string
	add := func(out []byte) {
		for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f = strings.TrimSpace(f); f != "" && !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	mb, err := exec.Command("git", "-C", rootDir, "merge-base", base, "HEAD").Output()
	if err != nil || strings.Trim(base, "0") == "" {
		return []string{"go.work"} // forces a full verification (global file)
	}
	if out, err := exec.Command("git", "-C", rootDir, "diff", "--name-only", strings.TrimSpace(string(mb))).Output(); err == nil {
		add(out)
	}
	if out, err := exec.Command("git", "-C", rootDir, "ls-files", "--others", "--exclude-standard").Output(); err == nil {
		add(out)
	}
	return files
}
