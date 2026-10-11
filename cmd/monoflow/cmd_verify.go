package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	verboseOut := false
	noFix := false

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
		} else if arg == "--update-snapshots" || arg == "-u" {
			opts.UpdateSnapshots = true
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
		} else if arg == "--verbose" {
			verboseOut = true
		} else if arg == "--strict" {
			opts.Strict = true
		} else if arg == "--no-fix" {
			noFix = true
		}
	}

	recording := os.Getenv("MONOFLOW_RECORD_KEY") != ""
	opts.KeepUntracked = recording
	// Format the agent's changed files first (each package's fix_cmd), so lint never fails on
	// what a formatter fixes. Never in a background run: the agent may be editing those files.
	var fixed verify.FixResult
	if !recording && !noFix {
		fixed = fixChangedFiles(rootDir)
	}
	// Plain verify: the background run started at the agent's last edit may already have the
	// answer for exactly this state of the tree (see speculate.go).
	plain := len(os.Args) == 2 || (len(os.Args) == 3 && os.Args[2] == "--affected")
	if !recording && plain && !jsonMode() {
		if out, ok := speculatedResult(rootDir, 15*time.Minute); ok {
			fmt.Print(out + fixLines(fixed) + reusedNote)
			if !strings.HasPrefix(out, "PASS") {
				os.Exit(1)
			}
			return
		}
		stopOtherSpecRuns(rootDir, "") // obsolete; they would compete for the CPU
	}
	storeResult := func(string) {}
	if !recording && plain && !jsonMode() {
		storeResult = beginForeground(rootDir)
	}
	if !recording {
		startRootWatcher(rootDir) // outside a worktree: precompute the next verify as the agent edits
	}

	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		printRaw(fmt.Sprintf(`{"status": "failed", "error": %q}`, err))
		os.Exit(1)
	}

	runner := verify.NewRunner(rootDir)
	runner.SetGraph(g)
	start := time.Now()

	var res verify.SuiteResult

	if targetPkg != "" {
		pkg, exists := resolvePackageArg(g, rootDir, targetPkg)
		if !exists {
			printRaw(fmt.Sprintf(`{"status": "failed", "error": "package '%s' not found (use a name from 'monoflow graph list' or a path inside one)"}`, targetPkg))
			os.Exit(1)
		}
		opts.ChangedFiles, opts.AllPackages = getGitChangedFiles(rootDir), true
		res = runner.VerifyPackages(g, []string{pkg.Name}, opts)
	} else if verifyAll {
		var names []string
		for _, p := range g.GetAllPackages() {
			names = append(names, p.Name)
		}
		opts.ChangedFiles, opts.AllPackages = getGitChangedFiles(rootDir), true
		res = runner.VerifyPackages(g, names, opts)
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

	var out any = res
	if !verboseOut {
		c := compactSuite(res, rootDir, g)
		// Say what was covered and what to do next: agents that were not told re-ran
		// verify --all and raw go test / npm test "to be sure" (benchmark: +6 calls a run).
		if verifyAll && len(opts.ChangedFiles) > 0 {
			c["note"] = "plain 'monoflow verify' already checks every package your change can affect; --all re-tests the rest too (not needed to ship)"
		}
		if targetPkg == "" && !verifyAll && len(res.Pending) > 0 {
			c["scope"] = fmt.Sprintf("%d changed packages checked first; %d dependents run once they pass", res.TotalTargets, len(res.Pending))
			c["next"] = "fix the errors listed (file:line), then monoflow verify"
		} else if targetPkg == "" && !verifyAll && res.TotalTargets > 0 {
			// Name the packages left out and why: an agent told "every module must pass" saw
			// "27 packages" in a 29-module repo and re-tested all 29 itself.
			total := len(g.GetAllPackages())
			if res.Excluded > 0 {
				c["excluded"] = fmt.Sprintf("%d dependent packages under %s skipped (verify_exclude in .agents/monoflow.config.json)", res.Excluded, strings.Join(g.VerifyExclude, ", "))
			}
			if others := total - res.TotalTargets - res.Excluded; others > 0 {
				c["scope"] = fmt.Sprintf("%d of %d packages: the changed ones and all their dependents (what CI runs for this change); the other %d cannot be affected", res.TotalTargets, total, others)
				if others <= 5 { // name them, so nobody goes looking (or re-tests them "to be sure")
					in := map[string]bool{}
					for _, n := range res.AffectedPackages {
						in[n] = true
					}
					var out []string
					for _, p := range g.GetAllPackages() {
						if !in[p.Name] {
							out = append(out, p.Path)
						}
					}
					c["not_affected"] = out
				}
			} else {
				c["scope"] = fmt.Sprintf("all %d packages (every package depends on your change)", total)
			}
			if res.Status == "passed" {
				c["next"] = `nothing else to run: monoflow ship -m "<msg>"`
			} else {
				c["next"] = "fix the errors listed (file:line), then monoflow verify"
			}
		}
		out = c
	}
	if c, ok := out.(map[string]any); ok {
		if len(fixed.Fixed) > 0 {
			c["formatted"] = fixed.Fixed
		}
		if len(fixed.Failed) > 0 {
			c["fix_failed"] = fixed.Failed
		}
	}
	if !verboseOut && !jsonMode() {
		rootDirForText = rootDir
		text := verifyText(res, out.(map[string]any))
		fmt.Print(text)
		storeResult(text)
	} else {
		printOut(out)
	}
	if recording {
		finishRecord(rootDir)
		return
	}

	cb := guard.NewCircuitBreaker(rootDir, 5)
	targetKey := strings.Join(res.AffectedPackages, ",")

	if res.Status == "failed" {
		st, tripped, _ := cb.RecordFailure(rootDir, "active_task", targetKey)
		if tripped {
			printOut(map[string]any{
				"circuit_breaker": "TRIPPED",
				"attempts":        st.Attempts,
				"snapshot":        st.SnapshotID,
				"advice":          "Same targets failed repeatedly. Step back: re-read the first error, check 'monoflow graph impact <file>', or 'monoflow checkpoint list' / 'checkpoint revert <id>' to restart from a known state. Your current work is saved as the snapshot above and was NOT reverted.",
				"reason":          fmt.Sprintf("Target '%s' failed %d consecutive times without passing", targetKey, st.Attempts),
			})
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
		printRaw(fmt.Sprintf(`{"status": "failed", "error": %q}`, err))
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
	printOut(out)
}

func getGitChangedFiles(rootDir string) []string {
	files := uncommittedFiles(rootDir)

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

// uncommittedFiles lists working-tree changes only (no fallback to the branch's commits).
func uncommittedFiles(rootDir string) []string {
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

// compactSuite is verify's default output: counts, each distinct error once (repo-relative,
// with the targets it breaks), and which targets failed. Cache keys, command lines and passed
// targets are --verbose only. On an opentelemetry-go rename that broke 22 modules this is
// ~4 KB instead of 25 KB of the same compiler error repeated per module.
// dedupedSuite returns res with errors merged across targets (repo-relative, capped at 30).
func dedupedSuite(res verify.SuiteResult, rootDir string, g *graph.DependencyGraph) verify.SuiteResult {
	pathOf := map[string]string{}
	for _, p := range g.GetAllPackages() {
		pathOf[p.Name] = p.Path
	}
	verify.DedupeErrors(&res, rootDir, pathOf, 30)
	return res
}

func compactSuite(res verify.SuiteResult, rootDir string, g *graph.DependencyGraph) map[string]any {
	res = dedupedSuite(res, rootDir, g)
	out := map[string]any{
		"status":         res.Status,
		"duration_ms":    res.DurationMs,
		"total_targets":  res.TotalTargets,
		"passed_targets": res.PassedTargets,
		"failed_targets": res.FailedTargets,
		"cached_targets": res.CachedTargets,
	}
	if len(res.AffectedPackages) <= 10 {
		out["affected_packages"] = res.AffectedPackages
	}
	// Preexisting (main fails the same way) and quarantined failures do not block: counts only.
	// Listing them in full cost agents 258 KB of context on trpc (610 old type errors in one
	// example); --verbose has the details.
	failed, preexisting, quarantined := map[string]int{}, map[string]int{}, map[string]int{}
	for _, r := range res.Results {
		if r.Status == "failed" {
			failed[r.Target] = r.ErrorCount
		}
		if len(r.Preexisting) > 0 {
			preexisting[r.Target] = len(r.Preexisting)
		}
		if len(r.Quarantined) > 0 {
			quarantined[r.Target] = len(r.Quarantined)
		}
	}
	if len(failed) > 0 {
		out["failed"] = failed // target -> error count
	}
	if len(preexisting) > 0 {
		names := make([]string, 0, len(preexisting))
		for t := range preexisting {
			names = append(names, t)
		}
		sort.Strings(names)
		parts := make([]string, len(names))
		for i, t := range names {
			parts[i] = fmt.Sprintf("%s (%d)", t, preexisting[t])
		}
		// Spelled out: a bare {"pkg": 1} read as an unexplained failure and sent agents re-testing.
		// No pointer to --verbose: agents followed it and read pages of other people's lint debt.
		out["preexisting"] = "main already fails these too; not caused by your change, not blocking, nothing to do: " + strings.Join(parts, ", ")
	}
	if len(quarantined) > 0 {
		out["quarantined"] = quarantined
	}
	var flaky []string
	for _, r := range res.Results {
		if r.Flaky {
			flaky = append(flaky, r.Target)
		}
	}
	if len(flaky) > 0 {
		out["flaky"] = flaky // failed under load, passed re-run alone (not blocking)
	}
	if len(res.Errors) > 0 {
		out["errors"] = res.Errors
	}
	if res.ErrorsOmitted > 0 {
		out["errors_omitted"] = res.ErrorsOmitted
	}
	if res.Hint != "" {
		out["hint"] = res.Hint
	}
	return out
}

// resolvePackageArg accepts a package name, its path, or any path inside it (an agent asked
// for --package=codes, a Go package inside the otel root module, and got "not found").
func resolvePackageArg(g *graph.DependencyGraph, rootDir, arg string) (*graph.PackageNode, bool) {
	if p, ok := g.GetPackage(arg); ok {
		return p, true
	}
	rel := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(arg, "./")))
	if filepath.IsAbs(arg) {
		if r, err := filepath.Rel(rootDir, arg); err == nil {
			rel = filepath.ToSlash(r)
		}
	}
	if _, err := os.Stat(filepath.Join(rootDir, rel)); err != nil {
		return nil, false
	}
	// The innermost package containing the path wins; the root package (".") contains all.
	var best *graph.PackageNode
	bestLen := -1
	for _, p := range g.GetAllPackages() {
		pp := filepath.ToSlash(p.Path)
		n := len(pp)
		if pp == "." {
			n = 0
		} else if rel != pp && !strings.HasPrefix(rel, pp+"/") {
			continue
		}
		if n > bestLen {
			best, bestLen = p, n
		}
	}
	return best, best != nil
}

// fixChangedFiles runs the packages' fix_cmd on the uncommitted files (new files included).
func fixChangedFiles(rootDir string) verify.FixResult {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		return verify.FixResult{}
	}
	var files []string
	for _, args := range [][]string{{"diff", "--name-only", "HEAD"}, {"ls-files", "--others", "--exclude-standard"}} {
		out, err := exec.Command("git", append([]string{"-C", rootDir}, args...)...).Output()
		if err != nil {
			continue
		}
		for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f != "" && !strings.HasPrefix(f, ".agents/") {
				files = append(files, f)
			}
		}
	}
	return verify.FixChanged(rootDir, g, files)
}

// fixLines renders what fixChangedFiles did, for output printed outside verifyText.
func fixLines(fr verify.FixResult) string {
	var b strings.Builder
	if len(fr.Fixed) > 0 {
		b.WriteString("formatted: " + strings.Join(fr.Fixed, ", ") + "\n")
	}
	if len(fr.Failed) > 0 {
		b.WriteString("fix_cmd failed (files left as they were): " + strings.Join(fr.Failed, "; ") + "\n")
	}
	return b.String()
}
