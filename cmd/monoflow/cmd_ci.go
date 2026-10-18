package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/cihealth"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	"github.com/antigravity/monoflow/pkg/proofs"
	"github.com/antigravity/monoflow/pkg/queue"
	"github.com/antigravity/monoflow/pkg/review"
	"github.com/antigravity/monoflow/pkg/verify"
)

func handleQuarantine(rootDir string) {
	qm := verify.NewQuarantineManager(rootDir)
	if len(os.Args) < 3 || os.Args[2] == "list" {
		printOut(qm.List())
		return
	}

	if os.Args[2] == "add" && len(os.Args) >= 5 {
		testID := os.Args[3]
		reason := strings.Join(os.Args[4:], " ")
		_ = qm.Quarantine(testID, reason, "manual quarantine")
		printRaw(fmt.Sprintf(`{"status": "quarantined", "test_id": %q}`, testID))
	}
}

func handleReview(rootDir string) {
	if len(os.Args) < 3 {
		printRaw(`{"error": "usage: monoflow review risk | audit --title=... --desc=... | throttle"}`)
		exit(1)
	}

	sub := os.Args[2]
	changedFiles := getGitChangedFiles(rootDir)
	g, _ := graph.LoadOrDiscover(rootDir)

	switch sub {
	case "risk":
		res, err := review.EvaluateRisk(rootDir, g, changedFiles)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, err))
			exit(1)
		}
		printOut(res)

	case "audit":
		title := "Agent Pull Request"
		desc := ""
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--title=") {
				title = strings.TrimPrefix(arg, "--title=")
			}
			if strings.HasPrefix(arg, "--desc=") {
				desc = strings.TrimPrefix(arg, "--desc=")
			}
		}
		res := review.AuditPRDossier(title, desc, changedFiles)
		printOut(res)
		if !res.Valid {
			exit(1)
		}

	case "throttle":
		status, err := review.CheckBackpressure(rootDir, 3)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, err))
			exit(1)
		}
		printOut(status)
		if status.Throttled {
			exit(1)
		}
	}
}

func handleQueue(rootDir string) {
	if len(os.Args) < 3 {
		printRaw(`{"error": "usage: monoflow queue spec [task-id] | verify <sha>"}`)
		exit(1)
	}

	qm := queue.NewQueueManager(rootDir)
	sub := os.Args[2]

	switch sub {
	case "spec", "submit":
		taskID := "task-queue-01"
		if len(os.Args) > 3 {
			taskID = os.Args[3]
		}
		g, _ := graph.LoadOrDiscover(rootDir)
		changedFiles := getGitChangedFiles(rootDir)

		receipt, err := qm.SubmitSpeculative(taskID, g, changedFiles, queue.SpeculativeOptions{
			TargetBranch:    "HEAD",
			SkipQuarantined: true,
		})
		if err != nil {
			printRaw(fmt.Sprintf(`{"status": "failed", "error": %q}`, err))
			exit(1)
		}
		printOut(receipt)

	case "verify":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing commit sha for queue verify"}`)
			exit(1)
		}
		sha := os.Args[3]
		eligible, receipt, reason := qm.VerifyCIGate(sha)
		printOut(map[string]any{
			"fast_path_eligible": eligible,
			"receipt":            receipt,
			"reason":             reason,
		})
		if !eligible {
			exit(1)
		}
	}
}

func handleCI(rootDir string) {
	if len(os.Args) > 3 && os.Args[2] == "proofs" {
		handleCIProofs(rootDir, os.Args[3])
		return
	}
	if len(os.Args) > 2 && os.Args[2] == "health" {
		handleCIHealth(rootDir)
		return
	}
	if len(os.Args) > 2 && os.Args[2] == "watch" {
		cw := events.NewCIWatcher(rootDir)
		opts := events.WatchOptions{
			Async: false,
		}
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--commit=") {
				opts.CommitSHA = strings.TrimPrefix(arg, "--commit=")
			} else if strings.HasPrefix(arg, "--branch=") {
				opts.Branch = strings.TrimPrefix(arg, "--branch=")
			} else if arg == "--async" {
				opts.Async = true
			}
		}
		job, err := cw.StartWatch(opts)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, err))
			exit(1)
		}
		printOut(job)
		return
	}

	qm := queue.NewQueueManager(rootDir)
	headSHA := ""
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--head=") {
			headSHA = strings.TrimPrefix(arg, "--head=")
		}
	}
	if headSHA == "" {
		cmdRev := exec.Command("git", "rev-parse", "HEAD")
		cmdRev.Dir = rootDir
		if out, err := cmdRev.CombinedOutput(); err == nil {
			headSHA = strings.TrimSpace(string(out))
		}
	}

	eligible, receipt, reason := qm.VerifyCIGate(headSHA)
	tierStr := "TIER_2_FULL_PIPELINE"
	if eligible {
		tierStr = "TIER_1_FAST_PATH"
	}
	printOut(map[string]any{
		"ci_tier":            tierStr,
		"fast_path_eligible": eligible,
		"head_sha":           headSHA,
		"reason":             reason,
		"receipt":            receipt,
	})
}

func handleCIHealth(rootDir string) {
	collectOpts := cihealth.CollectOptions{Limit: 100}
	checkFilter := ""
	withLogs := false
	for _, arg := range os.Args[3:] {
		switch {
		case strings.HasPrefix(arg, "--branch="):
			collectOpts.Branch = strings.TrimPrefix(arg, "--branch=")
		case strings.HasPrefix(arg, "--workflow="):
			collectOpts.Workflow = strings.TrimPrefix(arg, "--workflow=")
		case strings.HasPrefix(arg, "--limit="):
			if n, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit=")); err == nil && n > 0 {
				collectOpts.Limit = n
			}
		case strings.HasPrefix(arg, "--check="):
			checkFilter = strings.TrimPrefix(arg, "--check=")
		case arg == "--logs":
			withLogs = true
		}
	}
	if collectOpts.Branch == "" {
		collectOpts.Branch = "main"
		cmdHead := exec.Command("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		cmdHead.Dir = rootDir
		if out, err := cmdHead.Output(); err == nil {
			if b := strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/"); b != "" {
				collectOpts.Branch = b
			}
		}
	}

	if url, err := exec.Command("git", "-C", rootDir, "remote", "get-url", "origin").Output(); err != nil || !strings.Contains(string(url), "github") {
		// Not an error the agent can fix: say what exists instead of a gh API failure.
		printOut(map[string]any{"status": "unavailable",
			"reason": "ci health reads GitHub Actions history, and origin is not a GitHub remote",
			"next":   "local CI results of your commits are events: monoflow events list (or monoflow outer status)"})
		return
	}
	src := cihealth.NewGHSource(rootDir)
	data, warnings, err := cihealth.Collect(src, collectOpts)
	if err != nil {
		printOut(map[string]string{"error": err.Error(), "branch": collectOpts.Branch})
		exit(1)
	}
	rep := cihealth.Analyze(data, cihealth.Options{Branch: collectOpts.Branch, CheckFilter: checkFilter})
	rep.Warnings = append(warnings, rep.Warnings...)
	cihealth.Enrich(rep, src, cihealth.EnrichOptions{RootDir: rootDir, Logs: withLogs})

	printOut(rep)
}

func handleGuard(rootDir string) {
	testViolations, _ := guard.CheckTestIntegrity(rootDir)
	secretViolations, _ := guard.ScanSecrets(rootDir)

	allViolations := append(testViolations, secretViolations...)

	if len(allViolations) > 0 {
		printOut(map[string]any{
			"status":     "failed",
			"violations": allViolations,
			"verdict":    "Integrity or security violation detected. Commits or PRs with weakened tests or secrets are blocked.",
		})
		exit(1)
	}

	printOut(map[string]any{
		"status":          "passed",
		"test_integrity":  "verified (0 assertions deleted or commented out)",
		"secret_scanning": "clean (0 credentials or keys detected)",
		"verdict":         "All guard checks passed",
	})
}

func handleOuter(rootDir string) {
	om := verify.NewOuterLoopManager(rootDir)
	if len(os.Args) < 3 {
		printRaw(`{"error": "usage: monoflow outer start [opts] | status <id> | wait <id> | list"}`)
		exit(1)
	}

	sub := os.Args[2]
	switch sub {
	case "exec":
		// Internal: the detached process started by an async job.
		for _, a := range os.Args[3:] {
			if strings.HasPrefix(a, "--job=") {
				if err := om.ExecuteJobByID(strings.TrimPrefix(a, "--job=")); err != nil {
					exit(1)
				}
				return
			}
		}
		exit(2)

	case "start", "run", "check":
		opts := verify.OuterLoopOptions{
			AutoPush:   false,
			AutoRevert: false,
			Async:      false,
			IncludeE2E: true,
		}
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--commit=") {
				opts.CommitSHA = strings.TrimPrefix(arg, "--commit=")
			} else if strings.HasPrefix(arg, "--branch=") {
				opts.Branch = strings.TrimPrefix(arg, "--branch=")
			} else if strings.HasPrefix(arg, "--remote=") {
				opts.Remote = strings.TrimPrefix(arg, "--remote=")
			} else if strings.HasPrefix(arg, "--remote-branch=") {
				opts.RemoteBranch = strings.TrimPrefix(arg, "--remote-branch=")
			} else if arg == "--push" || arg == "--auto-push" {
				opts.AutoPush = true
			} else if arg == "--revert" || arg == "--auto-revert" {
				opts.AutoRevert = true
			} else if arg == "--async" {
				opts.Async = true
			} else if arg == "--no-e2e" {
				opts.IncludeE2E = false
			} else if arg == "--e2e" {
				opts.IncludeE2E = true
			}
		}

		job, err := om.StartJob(opts)
		if err != nil && job == nil {
			printRaw(fmt.Sprintf(`{"status": "failed", "error": %q}`, err))
			exit(1)
		}
		printOut(job)
		if job.Status == verify.JobRegressed {
			exit(1)
		}

	case "status":
		jobID := outerJobArg(rootDir, om)
		job, err := om.GetJob(jobID)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, err))
			exit(1)
		}
		printOuterJob(job)

	case "wait":
		jobID := outerJobArg(rootDir, om)
		timeout := 60 * time.Second
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--timeout=") {
				secStr := strings.TrimPrefix(arg, "--timeout=")
				if s, err := strconv.Atoi(strings.TrimSuffix(secStr, "s")); err == nil {
					timeout = time.Duration(s) * time.Second
				}
			}
		}
		job, err := om.WaitJob(jobID, timeout)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, err))
			exit(1)
		}
		printOuterJob(job)
		if job.Status == verify.JobRegressed {
			exit(1)
		}

	case "list":
		jobs, err := om.ListJobs()
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, err))
			exit(1)
		}
		printOut(jobs)

	default:
		printRaw(fmt.Sprintf(`{"error": "unknown outer subcommand: %s"}`, sub))
		exit(1)
	}
}

// handleCIProofs: `ci proofs import --commit=<sha>` (run by the generated workflow on PRs)
// and `ci proofs publish` (normally done automatically after a gated push).
func handleCIProofs(rootDir, sub string) {
	sha, remote := "", "origin"
	for _, a := range os.Args[4:] {
		if strings.HasPrefix(a, "--commit=") {
			sha = strings.TrimPrefix(a, "--commit=")
		} else if strings.HasPrefix(a, "--remote=") {
			remote = strings.TrimPrefix(a, "--remote=")
		}
	}
	if sha == "" {
		sha = strings.TrimSpace(gitOut(rootDir, "rev-parse", "HEAD"))
	}
	mgr := cache.NewManager(rootDir, cache.DefaultConfig(rootDir))
	switch sub {
	case "import":
		if !proofs.Trusted(rootDir) {
			printRaw(`{"status": "disabled", "reason": "ci.trust_local_proofs is false"}`)
			return
		}
		n, err := proofs.Import(rootDir, remote, sha, mgr)
		if err != nil {
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err.Error()))
			return // never fail CI because proofs are missing: it just verifies everything
		}
		printRaw(fmt.Sprintf(`{"status": "imported", "commit": %q, "records": %d}`, sha, n))
	case "publish":
		job := verify.NewOuterLoopManager(rootDir).LatestForCommit(sha)
		if job == nil || (job.Status != verify.JobPassed && job.Status != verify.JobPromoted) {
			printRaw(`{"status": "refused", "reason": "only a green outer loop for this commit can be published"}`)
			exit(1)
		}
		n, err := proofs.Publish(rootDir, remote, sha, mgr, verify.PassedKeys(job.TargetResults))
		if err != nil {
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err.Error()))
			exit(1)
		}
		printRaw(fmt.Sprintf(`{"status": "published", "commit": %q, "records": %d}`, sha, n))
	default:
		printRaw(`{"error": "usage: monoflow ci proofs import|publish [--commit=<sha>] [--remote=<r>]"}`)
		exit(1)
	}
}

// outerJobArg is the job id given on the command line, or the latest outer-loop job for HEAD:
// agents rarely keep the id from 'monoflow commit' around.
func outerJobArg(rootDir string, om *verify.OuterLoopManager) string {
	if len(os.Args) > 3 && !strings.HasPrefix(os.Args[3], "-") {
		return os.Args[3]
	}
	out, _ := exec.Command("git", "-C", rootDir, "rev-parse", "HEAD").Output()
	if job := om.LatestForCommit(strings.TrimSpace(string(out))); job != nil {
		return job.ID
	}
	printRaw(`{"error": "no outer-loop job for HEAD; 'monoflow commit' starts one (or pass a job id)"}`)
	exit(1)
	return ""
}

// printOuterJob prints the outcome an agent acts on; --verbose prints the whole job record.
func printOuterJob(job *verify.OuterJob) {
	if hasFlag("--verbose") {
		printOut(job)
		return
	}
	out := map[string]any{
		"id":          job.ID,
		"commit_sha":  job.CommitSHA,
		"status":      job.Status,
		"duration_ms": job.DurationMs,
		"packages":    fmt.Sprintf("%d/%d passed", job.PassedPackages, job.TotalPackagesTested),
	}
	var failed []verify.TargetResult
	for _, r := range job.TargetResults {
		if r.Status != "passed" {
			r.CacheKey, r.CachedFrom = "", ""
			failed = append(failed, r)
		}
	}
	if len(failed) > 0 {
		out["failed"] = failed
	}
	if job.E2ECheck != nil && job.E2ECheck.Status != "passed" {
		out["repo_ci"] = job.E2ECheck
	}
	for k, v := range map[string]string{"error_summary": job.ErrorSummary, "promote_error": job.PromoteError} {
		if v != "" {
			out[k] = v
		}
	}
	printOut(out)
}

func hasFlag(name string) bool {
	for _, a := range os.Args[2:] {
		if a == name {
			return true
		}
	}
	return false
}
