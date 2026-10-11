package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/conflict"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	"github.com/antigravity/monoflow/pkg/proofs"
	"github.com/antigravity/monoflow/pkg/verify"
)

func handleCommit(rootDir string) {
	refuseSharedRoot(rootDir, "commit")
	msg := "agent checkpoint commit"
	noOuter := false
	for i, arg := range os.Args[2:] {
		if arg == "-m" && i+1 < len(os.Args[2:]) {
			msg = os.Args[2:][i+1]
		}
		if arg == "--no-outer" {
			noOuter = true
		}
	}

	res, err := guard.FastAgentCommit(rootDir, msg)
	if err != nil {
		printOut(res)
		os.Exit(1)
	}

	out := map[string]any{"commit": res}
	// The outer loop (local CI on the exact commit) starts now, so it is usually green by
	// the time the agent pushes; push only happens once it is.
	outerJob := ""
	if !noOuter && res.CommitSHA != "" {
		om := verify.NewOuterLoopManager(rootDir)
		if job, err := om.StartJob(verify.OuterLoopOptions{CommitSHA: res.CommitSHA, Async: true, IncludeE2E: true}); err == nil {
			outerJob = job.ID
			out["outer_loop"] = map[string]any{"job": job.ID, "status": "running",
				"note": "local CI on this exact commit. Next: 'monoflow ship' (waits for it, pushes, opens the PR), or more edits + commit"}
		}
	}
	if !jsonMode() { // the agent wrote the message: do not echo it back
		fmt.Printf("committed %s\n", res.CommitSHA[:min(10, len(res.CommitSHA))])
		if outerJob != "" {
			fmt.Println("local CI: running on this commit")
		}
		fmt.Println("next: monoflow ship (pushes and opens the PR once local CI is green), or more edits + commit")
		return
	}
	printOut(out)
}

func handlePush(rootDir string) {
	// 1. Pre-push test integrity check & secret scan (< 20ms)
	testViolations, _ := guard.CheckTestIntegrity(rootDir)
	secretViolations, _ := guard.ScanSecrets(rootDir)
	allViolations := append(testViolations, secretViolations...)
	if len(allViolations) > 0 {
		printOut(map[string]any{
			"status":     "blocked",
			"violations": allViolations,
			"verdict":    "Pre-push integrity check failed. Cannot push weakened tests or credentials to remote.",
		})
		os.Exit(1)
	}

	remote := "origin"
	branch := ""
	noWatch := false
	forceDirect := false
	var positional []string

	skipVerify := false
	for _, arg := range os.Args[2:] {
		if arg == "--no-watch" {
			noWatch = true
		} else if arg == "--skip-verify" {
			skipVerify = true
		} else if arg == "--force" || arg == "--direct" {
			forceDirect = true
		} else if strings.HasPrefix(arg, "--remote=") {
			remote = strings.TrimPrefix(arg, "--remote=")
		} else if strings.HasPrefix(arg, "--branch=") {
			branch = strings.TrimPrefix(arg, "--branch=")
		} else if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}

	if len(positional) > 0 {
		remote = positional[0]
	}
	if len(positional) > 1 {
		branch = positional[1]
	}

	if branch == "" {
		cmdRev := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
		cmdRev.Dir = rootDir
		if out, err := cmdRev.CombinedOutput(); err == nil {
			branch = strings.TrimSpace(string(out))
		} else {
			branch = "main"
		}
	}

	// 1c. Protected Branch Shield: Block unverified direct pushes to main/master
	if (branch == "main" || branch == "master") && !forceDirect {
		printOut(map[string]any{
			"status":  "blocked",
			"verdict": fmt.Sprintf("Direct push to protected branch '%s' blocked. MonoFlow requires proposing changes via Pull Request to guarantee zero remote CI breakage. Use 'monoflow pr create' instead (or pass --force to bypass).", branch),
		})
		os.Exit(1)
	}

	headSHA := ""
	cmdSHA := exec.Command("git", "rev-parse", "HEAD")
	cmdSHA.Dir = rootDir
	if out, err := cmdSHA.CombinedOutput(); err == nil {
		headSHA = strings.TrimSpace(string(out))
	}

	// 1d. Outer-loop gate: the exact commit must have passed local CI (affected packages +
	// dependents + the repo's ci_cmd, in a clean checkout) before it is pushed.
	if !skipVerify {
		if dirty := uncommittedFiles(rootDir); len(dirty) > 0 && !onlyNonCode(dirty) {
			printOut(map[string]any{"status": "blocked",
				"verdict": "Uncommitted changes: commit them with 'monoflow commit' (which starts the outer loop) before pushing."})
			os.Exit(1)
		}
		om := verify.NewOuterLoopManager(rootDir)
		job := om.LatestForCommit(headSHA)
		switch {
		case job == nil:
			job, _ = om.StartJob(verify.OuterLoopOptions{CommitSHA: headSHA, Branch: branch, Remote: remote, RemoteBranch: branch, AutoPush: true, Async: true, IncludeE2E: true})
			printQueued(job, headSHA, branch)
			return
		case job.Status == verify.JobQueued || job.Status == verify.JobRunning:
			_ = om.QueuePush(job, remote, branch)
			printQueued(job, headSHA, branch)
			return
		case job.Status == verify.JobRegressed || job.Status == verify.JobRolledBack:
			printOut(map[string]any{
				"status":     "blocked",
				"verdict":    "Outer loop is red for this commit: remote CI would fail. Fix, 'monoflow commit', then push again.",
				"outer_loop": map[string]any{"job": job.ID, "failures": compactSummary(job.ErrorSummary)},
			})
			os.Exit(1)
		}
		// passed / promoted: push below
	}

	// 2. Publish proofs before the branch: an open PR starts CI on the push, and its import
	// step must find them (a proof ref does not reference the branch's objects).
	proofsPublished := 0
	if job := verify.NewOuterLoopManager(rootDir).LatestForCommit(headSHA); job != nil &&
		(job.Status == verify.JobPassed || job.Status == verify.JobPromoted) {
		if n, err := proofs.Publish(rootDir, remote, headSHA, cache.NewManager(rootDir, cache.DefaultConfig(rootDir)), verify.PassedKeys(job.TargetResults)); err == nil {
			proofsPublished = n
		}
	}

	// 3. Perform push: never with conflicts against the base.
	if err := conflict.GuardPush(rootDir, branch, "HEAD"); err != nil {
		printOut(map[string]any{"status": "conflict", "error": err.Error()})
		os.Exit(1)
	}
	start := time.Now()
	pushArgs := []string{"push", "-u", remote, branch}
	if forceDirect && branch != "main" && branch != "master" {
		pushArgs = []string{"push", "-u", "--force-with-lease", remote, branch}
	}
	cmdPush := exec.Command("git", pushArgs...)
	cmdPush.Dir = rootDir
	out, err := cmdPush.CombinedOutput()
	durMs := float64(time.Since(start).Microseconds()) / 1000.0

	if err != nil {
		printRaw(fmt.Sprintf(`{"status": "push_failed", "error": %q, "output": %q}`, err, strings.TrimSpace(string(out))))
		os.Exit(1)
	}

	conflict.StartWatch(rootDir, branch) // tell the agent if the base moves into a conflict later
	res := map[string]any{
		"status":      "pushed",
		"remote":      remote,
		"branch":      branch,
		"head_sha":    headSHA,
		"duration_ms": durMs,
	}

	// 3. Spawn autonomous background CI watcher if not disabled
	if !noWatch {
		cw := events.NewCIWatcher(rootDir)
		job, err := cw.StartWatch(events.WatchOptions{
			CommitSHA: headSHA,
			Branch:    branch,
			Remote:    remote,
			Async:     true, // Detach in background!
		})
		if err == nil {
			res["status"] = "pushed_and_watching"
			if proofsPublished > 0 {
				res["proofs_published"] = proofsPublished
			}
			res["watcher_id"] = job.ID
			res["ci_watcher_command"] = fmt.Sprintf("./bin/monoflow events watch %s --branch=%s", headSHA, branch)
			res["agent_action_required"] = "DO_NOT_WAIT"
			res["ci_wait_command"] = fmt.Sprintf("./bin/monoflow events wait %s", headSHA)
			res["agent_directive"] = ciWaitDirective(headSHA)
		}
	}

	printOut(res)
}

func handleSync(rootDir string) {
	start := time.Now()
	remote := "origin"
	branch := ""
	rebase := false
	autostash := false
	mergeMode := false
	stackedOn := ""

	var positional []string
	for _, arg := range os.Args[2:] {
		if arg == "--rebase" {
			rebase = true
		} else if arg == "--merge" {
			mergeMode = true
		} else if strings.HasPrefix(arg, "--stacked-on=") {
			// After the parent branch of a stacked PR was squash-merged, replay only this
			// branch's own commits onto the base.
			stackedOn = strings.TrimPrefix(arg, "--stacked-on=")
		} else if arg == "--autostash" {
			autostash = true
		} else if strings.HasPrefix(arg, "--remote=") {
			remote = strings.TrimPrefix(arg, "--remote=")
		} else if strings.HasPrefix(arg, "--branch=") {
			branch = strings.TrimPrefix(arg, "--branch=")
		} else if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
		}
	}

	if len(positional) > 0 {
		remote = positional[0]
	}
	if len(positional) > 1 {
		branch = positional[1]
	}

	current := "main"
	if out, err := exec.Command("git", "-C", rootDir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		current = strings.TrimSpace(string(out))
	}
	if branch == "" {
		// On a feature/agent branch "sync" means: bring my branch up to date with the base
		// branch (rebase). On the base branch itself it fast-forwards from the remote.
		base := "main"
		if out, err := exec.Command("git", "-C", rootDir, "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD").Output(); err == nil {
			base = strings.TrimPrefix(strings.TrimSpace(string(out)), remote+"/")
		} else if exec.Command("git", "-C", rootDir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/master").Run() == nil &&
			exec.Command("git", "-C", rootDir, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/main").Run() != nil {
			base = "master"
		}
		branch = base
		if current != base && !mergeMode {
			rebase = true
			autostash = true
		}
	}

	// 1. Fetch remote ref
	fetchArgs := []string{"fetch", remote, branch}
	cmdFetch := exec.Command("git", fetchArgs...)
	cmdFetch.Dir = rootDir
	if out, err := cmdFetch.CombinedOutput(); err != nil {
		printRaw(fmt.Sprintf(`{"status": "sync_failed", "step": "fetch", "error": %q, "output": %q}`, err.Error(), strings.TrimSpace(string(out))))
		os.Exit(1)
	}

	// 2. Fast-forward or Rebase
	var mergeArgs []string
	strategy := "fast-forward"
	if mergeMode {
		strategy = "merge"
		mergeArgs = []string{"merge", "--no-edit", "FETCH_HEAD"}
	} else if stackedOn != "" {
		strategy = "rebase-onto"
		rebase = true
		mergeArgs = []string{"rebase", "--autostash", "--onto", "FETCH_HEAD", stackedOn}
	} else if rebase {
		strategy = "rebase"
		mergeArgs = []string{"rebase"}
		if autostash {
			mergeArgs = append(mergeArgs, "--autostash")
		}
		mergeArgs = append(mergeArgs, "FETCH_HEAD")
	} else {
		mergeArgs = []string{"merge", "--ff-only", "FETCH_HEAD"}
	}

	cmdMerge := exec.Command("git", mergeArgs...)
	cmdMerge.Dir = rootDir
	if out, err := cmdMerge.CombinedOutput(); err != nil {
		res := map[string]any{"status": "sync_failed", "step": strategy, "error": err.Error(), "output": verify.CompactFailure(string(out))}
		conf, _ := exec.Command("git", "-C", rootDir, "diff", "--name-only", "--diff-filter=U").Output()
		res["conflicting_files"] = strings.Fields(string(conf))
		if mergeMode && len(res["conflicting_files"].([]string)) > 0 {
			// Conflicts are resolved once, in place: the markers stay for the agent.
			res["status"] = "conflicts"
			res["hint"] = "Resolve the <<<<<<< markers in conflicting_files (keep both sides' intent), run 'monoflow verify --affected', then 'monoflow commit -m \"merge " + branch + "\"' to finish the merge."
			printOut(res)
			os.Exit(1)
		}
		if rebase {
			// Never leave the worktree mid-rebase: report the conflicts and restore the branch.
			_ = exec.Command("git", "-C", rootDir, "rebase", "--abort").Run()
			res["hint"] = fmt.Sprintf("Your branch conflicts with %s/%s; branch left unchanged. Run 'monoflow sync --merge' to merge %s once and resolve the conflicts in place (a rebase would replay every commit).", remote, branch, branch)
		} else {
			res["hint"] = "Branches have diverged; run 'monoflow sync' from your agent branch (rebases onto the base) or pass --rebase."
		}
		printOut(res)
		os.Exit(1)
	}

	// 3. Get new HEAD SHA
	headSHA := ""
	cmdSHA := exec.Command("git", "rev-parse", "HEAD")
	cmdSHA.Dir = rootDir
	if out, err := cmdSHA.CombinedOutput(); err == nil {
		headSHA = strings.TrimSpace(string(out))
	}

	// 4. Check if sparse subsetting is active
	isSparse := false
	if _, err := os.Stat(filepath.Join(rootDir, ".agents", "subset.json")); err == nil {
		isSparse = true
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	res := map[string]any{
		"status":        "synced",
		"remote":        remote,
		"branch":        current,
		"onto":          remote + "/" + branch,
		"head_sha":      headSHA,
		"strategy":      strategy,
		"duration_ms":   durMs,
		"sparse_active": isSparse,
		"message":       "Workspace successfully synchronized with remote without raw git commands.",
	}

	printOut(res)
}

func currentBranch(rootDir string) string {
	out, err := exec.Command("git", "-C", rootDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refuseSharedRoot stops commands that edit files from running in the shared repository
// root while agent worktrees exist (they would change every agent's base checkout).
func refuseSharedRoot(rootDir, action string) {
	for _, a := range os.Args {
		if a == "--allow-root" {
			return
		}
	}
	gitDir, _ := exec.Command("git", "-C", rootDir, "rev-parse", "--git-dir").Output()
	common, _ := exec.Command("git", "-C", rootDir, "rev-parse", "--git-common-dir").Output()
	gd, cd := strings.TrimSpace(string(gitDir)), strings.TrimSpace(string(common))
	if gd == "" || gd != cd {
		return // inside a linked worktree: fine
	}
	entries, _ := os.ReadDir(filepath.Join(rootDir, ".agents", "worktrees"))
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	if len(ids) == 0 {
		return
	}
	printOut(map[string]any{
		"status": "refused",
		"error":  fmt.Sprintf("'%s' edits files, and this is the shared repository root used by agent worktrees (%s)", action, strings.Join(ids, ", ")),
		"hint":   "run it inside your worktree (.agents/worktrees/<agent-id>), or pass --allow-root if you really mean the root",
	})
	os.Exit(1)
}

func printQueued(job *verify.OuterJob, sha, branch string) {
	id := ""
	if job != nil {
		id = job.ID
	}
	res := map[string]any{
		"status":          "queued_until_outer_green",
		"branch":          branch,
		"head_sha":        sha,
		"outer_loop":      id,
		"verdict":         "The outer loop (local CI on this commit) is still running; it pushes automatically when green and then watches remote CI. Nothing is pushed if it fails.",
		"ci_wait_command": "./bin/monoflow events wait " + sha,
		"agent_directive": ciWaitDirective(sha),
	}
	if !jsonMode() {
		fmt.Printf("queued: pushes %s once local CI on %s is green (nothing is pushed if it fails)\nnext: end your turn; the result is delivered to you\n", branch, sha[:min(10, len(sha))])
		return
	}
	printOut(res)
}

func onlyNonCode(files []string) bool {
	for _, f := range files {
		if !graph.IsNonCodeFile(f) {
			return false
		}
	}
	return true
}

func compactSummary(s string) string {
	if len(s) > 1500 {
		return s[:1500] + " …"
	}
	return s
}

func gitOut(dir string, args ...string) string {
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out)
}
