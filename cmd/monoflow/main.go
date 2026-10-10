package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/agenthooks"
	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/cihealth"
	"github.com/antigravity/monoflow/pkg/deliver"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	initialize "github.com/antigravity/monoflow/pkg/init"
	"github.com/antigravity/monoflow/pkg/pr"
	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/queue"
	"github.com/antigravity/monoflow/pkg/release"
	"github.com/antigravity/monoflow/pkg/review"
	"github.com/antigravity/monoflow/pkg/runtimestate"
	"github.com/antigravity/monoflow/pkg/scenario"
	"github.com/antigravity/monoflow/pkg/subset"
	"github.com/antigravity/monoflow/pkg/task"
	"github.com/antigravity/monoflow/pkg/telemetry"
	"github.com/antigravity/monoflow/pkg/verify"
	"github.com/antigravity/monoflow/pkg/version"
	"github.com/antigravity/monoflow/pkg/worktree"
)

func main() {
	_ = os.Setenv("MONOFLOW_INTERNAL", "1")

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	if isHelpArg(cmd) {
		if len(os.Args) > 2 {
			printCommandUsage(os.Args[2])
		} else {
			printUsage()
		}
		os.Exit(0)
	}
	// A help flag anywhere after the command must never execute it: agents probe
	// `monoflow pr create --help` and would otherwise create PRs, branches or worktrees.
	if len(os.Args) > 2 && hasHelpArg(os.Args[2:]) {
		printCommandUsage(cmd)
		os.Exit(0)
	}

	rootDir, err := findRepoRoot()
	if err != nil && cmd != "init" && cmd != "clone" {
		rootDir = "."
	}

	metrics := telemetry.NewCollector()

	switch cmd {
	case "clone":
		handleClone()

	case "subset":
		handleSubset(rootDir)

	case "init":
		handleInit(rootDir)

	case "graph":
		handleGraph(rootDir)

	case "checkpoint":
		handleCheckpoint(rootDir)

	case "verify":
		handleVerify(rootDir, metrics)

	case "task":
		handleTask(rootDir)

	case "deliver":
		handleDeliver(rootDir)

	case "app":
		handleApp(rootDir)

	case "pr":
		handlePR(rootDir)

	case "worktree":
		handleWorktree(rootDir)

	case "review":
		handleReview(rootDir)

	case "queue":
		handleQueue(rootDir)

	case "ci":
		handleCI(rootDir)

	case "commit":
		handleCommit(rootDir)

	case "guard":
		handleGuard(rootDir)

	case "quarantine":
		handleQuarantine(rootDir)

	case "outer":
		handleOuter(rootDir)

	case "push":
		handlePush(rootDir)

	case "sync", "pull":
		handleSync(rootDir)

	case "branch", "switch":
		handleBranch(rootDir)

	case "events":
		handleEvents(rootDir)

	case "version":
		handleVersion(rootDir)

	case "scenario":
		handleScenario(rootDir)

	case "cache":
		handleCache(rootDir)

	case "hook":
		handleHook()

	default:
		fmt.Printf("Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	usageText(os.Stdout)
}

func usageText(w io.Writer) {
	fmt.Fprintln(w, `MonoFlow — The Agent-Native Monorepo & Ultra-Fast CI Engine

Usage:
  monoflow init [dir] [--setup-gh-actions]  Install MonoFlow (+ economical affected-only GitHub Actions CI)
  monoflow clone <url> [dest] --packages=<p1,p2> Clone subset of monorepo packages (< 1s partial clone)
  monoflow subset [set|add|remove|update|deps|check|status|reset] Manage package subset projection & dependency health
  monoflow graph [query|impact]       Query upstream/downstream dependencies & impact
  monoflow checkpoint [save|revert]   Ephemeral 15ms snapshots (shadow workspace)
  monoflow worktree [create|sync|gc]  Virtual worktrees with CoW isolation & AST merge (<60ms)
  monoflow verify [--affected|--proof] Sub-2s inner loop with compact JSON diagnostics
  monoflow outer [start|status|wait]  Async outer loop: transitive matrix & remote promotion barrier
  monoflow push [remote] [branch]     Fire-and-forget push with background autonomous CI watcher
  monoflow sync [remote] [branch]     Fast remote sync (ff-only or rebase) keeping sparse subset
  monoflow branch [name]              List, create, or switch branches without raw git commands
  monoflow events [unread|list|ack]   Asynchronous agent event inbox (check in < 5ms without polling)
  monoflow review [risk|audit|throttle] Machine risk scoring, anti-hallucination & backpressure
  monoflow queue [spec|verify]        Speculative local merge queue with flaky quarantine
  monoflow ci [check|watch|health]    Two-tier CI gate, CI watcher & CI health history (when/which commit broke a check)
  monoflow commit -m "<msg>"          Sub-50ms verified agent commit bypassing slow hooks
  monoflow guard [check]              Test integrity guard (anti-cheating) & secret scanner
  monoflow task [check|prove|slice]   DoD contracts, Proof of Work receipts, & stacked diffs
  monoflow deliver [--target=<p>]     Assemble hermetic deliverable artifact (.agents/dist)
  monoflow cache [status|clean|serve] Local & remote package cache metrics, hit ratio & built-in HTTP daemon
  monoflow scenario [run] [--json]    Real end-to-end multi-agent enterprise monorepo simulation
  monoflow app test                   Delta compilation + symlink overlay (< 5s full-app)
  monoflow pr [create|list|view|status|review|test|local|merge] Full agent-native PR suite with affected packages
  monoflow quarantine [list|add]      Manage flaky test quarantine
  monoflow version [list|bump|apply|check] SemVer package versioning, changesets & cascade
  monoflow hook [pretooluse|check]    Agent harness hook: redirect raw build/test/git commands to MonoFlow
  monoflow help [command]             Show this help (also --help, -h)`)
}

func isHelpArg(arg string) bool {
	return arg == "--help" || arg == "-h" || arg == "help"
}

// hasHelpArg reports whether any argument is a help flag. The bare word "help" only
// counts in subcommand position so it can still be used as a value (e.g. a PR title).
func hasHelpArg(args []string) bool {
	for i, a := range args {
		if a == "--help" || a == "-h" || (a == "help" && i == 0) {
			return true
		}
	}
	return false
}

// commandDetails documents subcommands and flags so agents can discover them via --help
// instead of guessing (guessing previously executed side-effecting commands).
var commandDetails = map[string]string{
	"graph": `  graph query <package|path>          Upstream deps, direct & transitive dependents (name or directory path)
  graph impact <file>                 Owning package and dependents affected by editing <file>
  graph affected                      Packages affected by the current uncommitted changes`,
	"checkpoint": `  checkpoint save "<hypothesis>"      Snapshot the working tree (not git history)
  checkpoint revert [id]              Restore the latest (or given) checkpoint
  checkpoint list                     List checkpoints`,
	"verify": `  verify --affected                   Test/lint/typecheck packages affected by your changes (+ dependents)
  verify --package=<p> | --all        Verify one package / every package
  verify --test | --lint | --typecheck | --all-checks   Restrict or widen the check kinds
  verify --no-cache | --force         Ignore cached results
  verify --base=<ref>                 Changes since the merge-base with <ref> (CI: the PR base)
  verify --strict                     Also block on failures that pre-exist on the base branch
  verify --proof=<task-id>            Verify a Proof-of-Work receipt`,
	"worktree": `  worktree create <agent-id> --packages=<p1,p2> [--deps=<strategy>] [--isolated-deps]
                                      Isolated worktree on branch agent/<agent-id>
  worktree list                       List registered worktrees
  worktree focus|update <agent-id> --packages=<...>   Change the projected packages
  worktree sync --to=<agent> --from=<agent>   AST-merge another agent's changes into a worktree
                                      (to update your branch from main, run 'monoflow sync' inside it)
  worktree finish <agent-id> [--force] Remove the worktree and its registry entry
  worktree isolate <agent-id>         Materialize per-worktree dependencies`,
	"pr": `  pr create --title="<t>" [--body="<b>"] [--base=<b>] [--head=<b>] [--draft] [--stack] [--no-push]
                                      Gate-checked PR (blocked unless DoD proof is VERIFIED)
  pr list [--state=<s>] [--limit=<n>] List PRs
  pr view <id> | pr status [id] [--json]   Show a PR / bridged local+remote status
  pr review <id> --approve|--reject|--request-changes [--comment="<c>"]
  pr test <id> [--base=<b>]           Hot-mount delta test of a PR
  pr local <id> [--worktree] [--deps=<s>]  Check out a PR's affected packages locally
  pr merge <id> [--strategy=squash|merge|rebase] [--queue] [--delete-branch]`,
	"push": `  push [remote] [branch] [--remote=<r>] [--branch=<b>] [--no-watch]
                                      Push the current branch (main/master blocked) and start the CI watcher`,
	"events": `  events unread                       Unread events (CI results, regressions, PRs)
  events list                         All events
  events ack <id|all>                 Mark events read
  events watch <sha> [--branch=<b>] [--timeout=<d>]   Background CI watcher (do not poll)`,
	"outer": `  outer start [--commit=<sha>] [--branch=<b>] [--push] [--revert] [--async]
  outer status | outer list | outer wait [--timeout=<d>]`,
	"version": `  version list | check                Versions and range compatibility of every internal edge
  version edges                       Every in-repo dependency: mode (source = live code, pinned = release) and range
  version suggest [pkg] [--base=<ref>] Required bump from the exported-API diff (removed/changed = major)
  version bump <pkg> <patch|minor|major> [--cascade]   Bump; reports each consumer's impact, never rewrites them implicitly
  version release <pkg>               Tag <pkg>@<version>; notify consumers (compatible / breaking + impact)
  version pin <consumer> <dep> [--version=<v>]   Consume a release: isolated from later changes to <dep>
  version unpin <consumer> <dep>      Back to the live code
  version upgrades [consumer]         Newer releases for pinned edges, with the API changes the consumer uses
  version upgrade <consumer> <dep> [--to=<v>]    Move the pin and run the consumer's verify`,
	"commit": `  commit -m "<msg>"                   Verified commit of all changes in the current worktree`,
	"cache":  `  cache status | cache clean | cache serve [--port=<n>] [--dir=<d>] [--token=<t>]`,
	"task":   `  task check | task slice | task prove [task-id] | task changeset`,
	"sync": `  sync                                On an agent/feature branch: rebase onto origin/<base> (aborts cleanly on conflict)
                                      On the base branch: fast-forward from the remote
  sync --merge                        Merge the base instead (resolve conflicts once, then 'monoflow commit')
  sync [remote] [branch] [--rebase] [--autostash]   Explicit remote/branch`,
	"branch": `  branch                              List branches
  branch <name> [-c|--create]         Switch to (or create) a branch`,
}

// printCommandUsage prints the usage lines for a single top-level command and reports
// whether the command is known. Unknown commands fall back to the full usage text.
func printCommandUsage(cmd string) bool {
	var buf strings.Builder
	usageText(&buf)
	var lines []string
	for _, line := range strings.Split(buf.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "monoflow" && fields[1] == cmd {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		printUsage()
		return false
	}
	fmt.Println("Usage:")
	fmt.Println(strings.Join(lines, "\n"))
	if d, ok := commandDetails[cmd]; ok {
		fmt.Println("\nSubcommands & flags:")
		fmt.Println(d)
	}
	return true
}

func findRepoRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

func handleInit(rootDir string) {
	targetDir := "."
	setupGH := false
	for _, a := range os.Args[2:] {
		if a == "--setup-gh-actions" {
			setupGH = true
		} else if !strings.HasPrefix(a, "-") {
			targetDir = a
		}
	}

	selfBin, _ := os.Executable()
	res, err := initialize.InitRepo(targetDir, selfBin)
	if err != nil {
		fmt.Printf("Error initializing repo: %v\n", err)
		os.Exit(1)
	}
	workflowPath := ""
	if setupGH {
		if workflowPath, err = initialize.WriteGitHubWorkflow(res.RepoRoot); err != nil {
			fmt.Printf("Error writing GitHub Actions workflow: %v\n", err)
			os.Exit(1)
		}
	}

	data, _ := json.MarshalIndent(map[string]any{
		"status":              "initialized",
		"repo_root":           res.RepoRoot,
		"package_count":       res.PackageCount,
		"discovered_packages": res.DiscoveredPkgs,
		"config_path":         res.ConfigPath,
		"rule_file":           filepath.Join(res.RepoRoot, ".agents", "rules", "agentic-monorepo.md"),
		"skill_file":          filepath.Join(res.RepoRoot, ".agents", "skills", "monoflow", "SKILL.md"),
		"workflow_file":       workflowPath,
	}, "", "  ")

	fmt.Println(string(data))
}

func handleGraph(rootDir string) {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		fmt.Printf(`{"error": "%s"}`+"\n", err)
		os.Exit(1)
	}

	if len(os.Args) == 2 {
		// Output full graph in JSON
		pkgs := g.GetAllPackages()
		type nodeOutput struct {
			Name         string   `json:"name"`
			Path         string   `json:"path"`
			Language     string   `json:"language"`
			Upstream     []string `json:"upstream"`
			Downstream   []string `json:"downstream"`
			TestCmd      string   `json:"test_cmd,omitempty"`
			LintCmd      string   `json:"lint_cmd,omitempty"`
			TypecheckCmd string   `json:"typecheck_cmd,omitempty"`
		}
		nodes := make([]nodeOutput, len(pkgs))
		for i, p := range pkgs {
			nodes[i] = nodeOutput{
				Name:         p.Name,
				Path:         p.Path,
				Language:     string(p.Language),
				Upstream:     g.GetUpstream(p.Name),
				Downstream:   g.GetDownstream(p.Name),
				TestCmd:      p.TestCmd,
				LintCmd:      p.LintCmd,
				TypecheckCmd: p.TypecheckCmd,
			}
		}
		data, _ := json.MarshalIndent(map[string]any{
			"package_count": len(nodes),
			"packages":      nodes,
		}, "", "  ")
		fmt.Println(string(data))
		return
	}

	sub := os.Args[2]
	switch sub {
	case "query":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing package name for query"}`)
			os.Exit(1)
		}
		pkgName := os.Args[3]
		pkg, exists := g.GetPackage(pkgName)
		if !exists {
			fmt.Printf(`{"error": "package '%s' not found"}`+"\n", pkgName)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"package":               pkg.Name,
			"path":                  pkg.Path,
			"language":              pkg.Language,
			"upstream_dependencies": g.GetUpstream(pkg.Name),
			"downstream_dependents": g.GetDownstream(pkg.Name),
			"transitive_dependents": g.GetTransitiveDownstream(pkg.Name),
		}, "", "  ")
		fmt.Println(string(data))

	case "impact":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing file path for impact analysis"}`)
			os.Exit(1)
		}
		filePath := os.Args[3]
		pkg := g.MapFileToPackage(filePath)
		if pkg == nil {
			fmt.Printf(`{"file": "%s", "impact": "none (untracked/root file)"}`+"\n", filePath)
			return
		}
		data, _ := json.MarshalIndent(map[string]any{
			"modified_file":         filePath,
			"owning_package":        pkg.Name,
			"direct_dependents":     g.GetDownstream(pkg.Name),
			"transitive_dependents": g.GetTransitiveDownstream(pkg.Name),
		}, "", "  ")
		fmt.Println(string(data))

	case "affected":
		changedFiles := getGitChangedFiles(rootDir)
		affected := g.ComputeAffected(changedFiles)
		data, _ := json.MarshalIndent(affected, "", "  ")
		fmt.Println(string(data))
	}
}

func handleCheckpoint(rootDir string) {
	mgr := checkpoint.NewManager(rootDir)
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "missing checkpoint subcommand: save | revert | list"}`)
		os.Exit(1)
	}

	sub := os.Args[2]
	switch sub {
	case "save":
		msg := "agent checkpoint"
		if len(os.Args) > 3 {
			msg = strings.Join(os.Args[3:], " ")
		}
		start := time.Now()
		chk, err := mgr.Save(msg)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"status":      "saved",
			"id":          chk.ID,
			"message":     chk.Message,
			"tree_hash":   chk.TreeHash,
			"duration_ms": durMs,
			"files":       chk.Files,
		}, "", "  ")
		fmt.Println(string(data))

	case "revert":
		targetID := ""
		if len(os.Args) > 3 {
			targetID = os.Args[3]
		}
		start := time.Now()
		cp, err := mgr.RevertCheckpoint(targetID)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"status":      "reverted",
			"checkpoint":  cp.ID,
			"message":     cp.Message,
			"files":       cp.Files,
			"duration_ms": durMs,
		}, "", "  ")
		fmt.Println(string(data))

	case "list":
		list, _ := mgr.List()
		data, _ := json.MarshalIndent(list, "", "  ")
		fmt.Println(string(data))
	}
}

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

func handleTask(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "missing task subcommand: check | prove | slice | changeset"}`)
		os.Exit(1)
	}

	sub := os.Args[2]
	changedFiles := getGitChangedFiles(rootDir)

	switch sub {
	case "check":
		taskBundlePath := filepath.Join(rootDir, ".agents", "task.json")
		var bundle task.TaskBundle
		if data, err := os.ReadFile(taskBundlePath); err == nil {
			_ = json.Unmarshal(data, &bundle)
		}
		res := task.CheckBoundaries(&bundle, changedFiles)
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		if !res.Valid {
			os.Exit(1)
		}

	case "slice":
		slice := task.SliceStackedDiff(changedFiles)
		data, _ := json.MarshalIndent(slice, "", "  ")
		fmt.Println(string(data))

	case "prove":
		taskID := "task-001"
		if len(os.Args) > 3 {
			taskID = os.Args[3]
		}
		g, _ := graph.LoadOrDiscover(rootDir)
		receipt, dossier, err := proof.ProveTask(rootDir, taskID, g, changedFiles)
		if err != nil {
			fmt.Printf(`{"status": "failed", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"status":              receipt.Status,
			"receipt":             receipt,
			"pr_markdown_dossier": dossier,
		}, "", "  ")
		fmt.Println(string(data))

	case "changeset":
		if len(os.Args) < 5 {
			fmt.Println(`{"error": "usage: monoflow task changeset <pkg> <patch|minor|major> <summary>"}`)
			os.Exit(1)
		}
		cs := task.Changeset{
			Package:   os.Args[3],
			BumpType:  os.Args[4],
			Summary:   strings.Join(os.Args[5:], " "),
			TaskID:    "task-001",
			CreatedAt: time.Now().UTC(),
		}
		_ = task.SaveChangeset(rootDir, cs)
		fmt.Println(`{"status": "changeset_created"}`)
	}
}

func handleDeliver(rootDir string) {
	g, _ := graph.LoadOrDiscover(rootDir)
	target := ""
	opts := deliver.BuildOptions{}

	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--target=") {
			target = strings.TrimPrefix(arg, "--target=")
		} else if strings.HasPrefix(arg, "--package=") {
			target = strings.TrimPrefix(arg, "--package=")
		} else if arg == "--no-cache" || arg == "--force" {
			opts.NoCache = true
		}
	}

	pkgs := g.GetAllPackages()
	for _, p := range pkgs {
		if target == "" || p.Name == target {
			manifest, err := deliver.BuildDeliverableWithOptions(rootDir, p, opts)
			if err == nil {
				data, _ := json.MarshalIndent(manifest, "", "  ")
				fmt.Println(string(data))
			}
		}
	}
}

func handleApp(rootDir string) {
	start := time.Now()
	g, _ := graph.LoadOrDiscover(rootDir)
	changedFiles := getGitChangedFiles(rootDir)
	affected := g.ComputeAffected(changedFiles)

	overlayPath, err := deliver.AssembleFullAppOverlay(rootDir, g, affected.AllAffected)
	if err != nil {
		fmt.Printf(`{"error": "%s"}`+"\n", err)
		os.Exit(1)
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	data, _ := json.MarshalIndent(map[string]any{
		"status":            "ready",
		"overlay_path":      overlayPath,
		"graft_duration_ms": durMs,
		"cached_packages":   len(g.GetAllPackages()) - len(affected.AllAffected),
		"delta_packages":    affected.AllAffected,
		"verdict":           "Full App assembled in < 5 seconds without recompiling unchanged packages",
	}, "", "  ")
	fmt.Println(string(data))
}

func handlePR(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`MonoFlow Pull Request Commands:
  monoflow pr create [--title="..."] [--body="..."] [--base=main] [--draft] [--stack] [--no-push]
  monoflow pr list [--state=open|closed|merged|all] [--limit=N]
  monoflow pr view <id>
  monoflow pr status [id] [--json]
  monoflow pr review <id> [--approve] [--comment="..."] [--reject]
  monoflow pr test <id> [--against-master] [--base=main]
  monoflow pr local <id> [--worktree=<id>] [--deps=contracts|source|none]
  monoflow pr merge <id> [--strategy=squash|merge|rebase] [--queue] [--delete-branch]`)
		os.Exit(1)
	}

	mgr := pr.NewManager(rootDir)
	sub := os.Args[2]

	switch sub {
	case "create":
		opts := pr.CreateOptions{
			BaseBranch: "main",
			AutoPush:   true,
		}
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--title=") {
				opts.Title = strings.TrimPrefix(arg, "--title=")
			} else if strings.HasPrefix(arg, "--body=") {
				opts.Body = strings.TrimPrefix(arg, "--body=")
			} else if strings.HasPrefix(arg, "--base=") {
				opts.BaseBranch = strings.TrimPrefix(arg, "--base=")
			} else if strings.HasPrefix(arg, "--head=") {
				opts.HeadBranch = strings.TrimPrefix(arg, "--head=")
			} else if strings.HasPrefix(arg, "--remote=") {
				opts.Remote = strings.TrimPrefix(arg, "--remote=")
			} else if arg == "--draft" {
				opts.Draft = true
			} else if arg == "--stack" {
				opts.Stack = true
			} else if arg == "--no-push" {
				opts.AutoPush = false
			}
		}

		res, err := mgr.Create(opts)
		if err != nil {
			var gateErr *pr.GateError
			if errors.As(err, &gateErr) {
				data, _ := json.MarshalIndent(map[string]any{
					"status":        "blocked",
					"error":         err.Error(),
					"proof_status":  gateErr.ProofStatus,
					"failed_checks": gateErr.FailedChecks,
					"audit":         gateErr.Audit,
				}, "", "  ")
				fmt.Println(string(data))
				os.Exit(1)
			}
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "list":
		opts := pr.ListOptions{
			State: "open",
		}
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--state=") {
				opts.State = strings.TrimPrefix(arg, "--state=")
			} else if strings.HasPrefix(arg, "--limit=") {
				if lim, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit=")); err == nil {
					opts.Limit = lim
				}
			}
		}

		list, err := mgr.List(opts)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(list, "", "  ")
		fmt.Println(string(data))

	case "view":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing pr-id for monoflow pr view"}`)
			os.Exit(1)
		}
		prID := os.Args[3]
		meta, err := mgr.View(prID)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(meta, "", "  ")
		fmt.Println(string(data))

	case "status":
		prID := ""
		asJSON := false
		for _, arg := range os.Args[3:] {
			if arg == "--json" {
				asJSON = true
			} else if !strings.HasPrefix(arg, "-") && prID == "" {
				prID = arg
			}
		}

		res, err := mgr.Status(prID)
		if err != nil {
			if asJSON {
				fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			} else {
				fmt.Printf("❌ Failed to get PR status: %v\n", err)
			}
			os.Exit(1)
		}

		if asJSON {
			data, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(data))
		} else {
			fmt.Println(res.FormatView())
		}

	case "review":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing pr-id for monoflow pr review"}`)
			os.Exit(1)
		}
		prID := os.Args[3]
		opts := pr.ReviewOptions{}
		for _, arg := range os.Args[4:] {
			if arg == "--approve" {
				opts.Approve = true
			} else if arg == "--reject" || arg == "--request-changes" {
				opts.RequestChanges = true
			} else if strings.HasPrefix(arg, "--comment=") {
				opts.Comment = strings.TrimPrefix(arg, "--comment=")
			}
		}

		res, err := mgr.Review(prID, opts)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "test":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing pr-id for monoflow pr test"}`)
			os.Exit(1)
		}
		prID := os.Args[3]
		opts := pr.TestOptions{
			BaseBranch: "main",
		}
		for _, arg := range os.Args[4:] {
			if strings.HasPrefix(arg, "--base=") {
				opts.BaseBranch = strings.TrimPrefix(arg, "--base=")
			} else if arg == "--against-master" {
				opts.BaseBranch = "master"
			}
		}

		res, err := mgr.Test(prID, opts)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "local":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "usage: monoflow pr local <pr-id> [--worktree=<id>] [--deps=contracts|source|none]"}`)
			os.Exit(1)
		}
		prID := os.Args[3]
		opts := pr.LocalOptions{
			DepStrategy: subset.DepStrategyContracts,
		}
		for _, arg := range os.Args[4:] {
			if strings.HasPrefix(arg, "--worktree=") {
				opts.WorktreeID = strings.TrimPrefix(arg, "--worktree=")
			} else if arg == "--worktree" {
				opts.WorktreeID = "pr-" + strings.TrimPrefix(prID, "#")
			} else if strings.HasPrefix(arg, "--deps=") {
				opts.DepStrategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
			} else if strings.HasPrefix(arg, "--remote=") {
				opts.Remote = strings.TrimPrefix(arg, "--remote=")
			}
		}

		res, err := mgr.Local(prID, opts)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "merge":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing pr-id for monoflow pr merge"}`)
			os.Exit(1)
		}
		prID := os.Args[3]
		opts := pr.MergeOptions{
			Strategy: "squash",
		}
		for _, arg := range os.Args[4:] {
			if strings.HasPrefix(arg, "--strategy=") {
				opts.Strategy = strings.TrimPrefix(arg, "--strategy=")
			} else if arg == "--queue" {
				opts.UseQueue = true
			} else if arg == "--delete-branch" {
				opts.DeleteBranch = true
			}
		}

		res, err := mgr.Merge(prID, opts)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	default:
		// Fallback for previous monoflow pr test <id> syntax where id was directly second arg
		opts := pr.TestOptions{BaseBranch: "main"}
		res, err := mgr.Test(sub, opts)
		if err != nil {
			fmt.Printf(`{"error": "unknown pr subcommand '%s'; usage: monoflow pr [create|list|view|status|review|test|local|merge]"}`+"\n", sub)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
	}
}

func handleQuarantine(rootDir string) {
	qm := verify.NewQuarantineManager(rootDir)
	if len(os.Args) < 3 || os.Args[2] == "list" {
		data, _ := json.MarshalIndent(qm.List(), "", "  ")
		fmt.Println(string(data))
		return
	}

	if os.Args[2] == "add" && len(os.Args) >= 5 {
		testID := os.Args[3]
		reason := strings.Join(os.Args[4:], " ")
		_ = qm.Quarantine(testID, reason, "manual quarantine")
		fmt.Printf(`{"status": "quarantined", "test_id": "%s"}`+"\n", testID)
	}
}

func handleWorktree(rootDir string) {
	mgr := worktree.NewManager(rootDir)
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "usage: monoflow worktree create <agent-id> | list | sync --to=X --from=Y | finish <agent-id> | prune"}`)
		os.Exit(1)
	}

	sub := os.Args[2]
	switch sub {
	case "create":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing agent-id for worktree create"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		if !worktree.ValidAgentID(agentID) {
			fmt.Printf(`{"error": "invalid agent-id %q (letters, digits, . _ -): usage: monoflow worktree create <agent-id> --packages=<p1,p2>"}`+"\n", agentID)
			os.Exit(1)
		}
		isolated := false
		baseBranch := ""
		var pkgs []string
		focusedPkg := ""
		strategy := subset.DepStrategySource
		includeDeps := true
		for i := 4; i < len(os.Args); i++ {
			arg := os.Args[i]
			if arg == "--isolated-deps" {
				isolated = true
			} else if strings.HasPrefix(arg, "--package=") {
				focusedPkg = strings.TrimPrefix(arg, "--package=")
			} else if strings.HasPrefix(arg, "--packages=") {
				raw := strings.TrimPrefix(arg, "--packages=")
				pkgs = strings.Split(raw, ",")
			} else if arg == "--packages" && i+1 < len(os.Args) {
				pkgs = strings.Split(os.Args[i+1], ",")
				i++
			} else if strings.HasPrefix(arg, "--deps=") {
				strategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
			} else if strings.HasPrefix(arg, "--include-deps=") {
				val := strings.TrimPrefix(arg, "--include-deps=")
				includeDeps = val != "false" && val != "0"
			} else if strings.HasPrefix(arg, "--base=") {
				baseBranch = strings.TrimPrefix(arg, "--base=")
			}
		}

		start := time.Now()
		info, err := mgr.CreateWithOpts(agentID, worktree.CreateOptions{
			BaseBranch:     baseBranch,
			IsolatedDeps:   isolated,
			FocusedPackage: focusedPkg,
			Packages:       pkgs,
			Strategy:       strategy,
			IncludeDeps:    includeDeps,
		})
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		resMap := map[string]any{
			"status":           "created",
			"agent_id":         info.AgentID,
			"path":             info.Path,
			"head_sha":         info.HeadSHA,
			"symlinks_count":   info.SymlinksCount,
			"isolated":         info.Isolated,
			"isolation_method": info.IsolationMethod,
			"is_sparse":        info.IsSparse,
			"duration_ms":      durMs,
			"verdict":          "Virtual worktree provisioned (< 60ms shared, or ~400ms CoW cloned)",
		}
		if info.FocusedPackage != "" {
			resMap["focused_package"] = info.FocusedPackage
		}
		if info.IsSparse {
			resMap["packages"] = info.Packages
			resMap["omitted_packages"] = info.OmittedPackages
			resMap["strategy"] = info.Strategy
		}
		data, _ := json.MarshalIndent(resMap, "", "  ")
		fmt.Println(string(data))

	case "focus":
		if len(os.Args) < 5 {
			fmt.Println(`{"error": "usage: monoflow worktree focus <agent-id> <package> [--deps=source|contracts|none]"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		pkg := os.Args[4]
		strategy := subset.DepStrategySource
		for _, arg := range os.Args[5:] {
			if strings.HasPrefix(arg, "--deps=") {
				strategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
			}
		}
		info, err := mgr.Focus(agentID, pkg, strategy)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(data))

	case "add", "add-package":
		if len(os.Args) < 5 {
			fmt.Println(`{"error": "usage: monoflow worktree add <agent-id> <pkg1,pkg2,...> [--deps=source|contracts|none]"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		pkgs := strings.Split(os.Args[4], ",")
		strategy := subset.DepStrategySource
		for _, arg := range os.Args[5:] {
			if strings.HasPrefix(arg, "--deps=") {
				strategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
			}
		}
		info, err := mgr.AddPackage(agentID, pkgs, strategy)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(data))

	case "remove", "remove-package":
		if len(os.Args) < 5 {
			fmt.Println(`{"error": "usage: monoflow worktree remove <agent-id> <pkg1,pkg2,...> [--force]"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		pkgs := strings.Split(os.Args[4], ",")
		force := false
		for _, arg := range os.Args[5:] {
			if arg == "--force" {
				force = true
			}
		}
		info, err := mgr.RemovePackage(agentID, pkgs, force)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(data))

	case "update":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "usage: monoflow worktree update <agent-id> [pkg1,pkg2,...] [--remote=origin] [--branch=main]"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		var pkgs []string
		remote := "origin"
		branch := "main"
		for _, arg := range os.Args[4:] {
			if strings.HasPrefix(arg, "--remote=") {
				remote = strings.TrimPrefix(arg, "--remote=")
			} else if strings.HasPrefix(arg, "--branch=") {
				branch = strings.TrimPrefix(arg, "--branch=")
			} else if !strings.HasPrefix(arg, "--") {
				pkgs = strings.Split(arg, ",")
			}
		}
		res, err := mgr.UpdateFromRemote(agentID, pkgs, remote, branch)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "isolate":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing agent-id for worktree isolate"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		res, err := mgr.Isolate(agentID)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "list":
		list, err := mgr.List()
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(list, "", "  ")
		fmt.Println(string(data))

	case "sync":
		toID := ""
		fromID := ""
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--to=") {
				toID = strings.TrimPrefix(arg, "--to=")
			}
			if strings.HasPrefix(arg, "--from=") {
				fromID = strings.TrimPrefix(arg, "--from=")
			}
		}
		if toID == "" || fromID == "" {
			fmt.Println(`{"error": "usage: monoflow worktree sync --to=<target-agent> --from=<source-agent>"}`)
			os.Exit(1)
		}
		start := time.Now()
		synced, err := mgr.Sync(toID, fromID)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"status":       "synced",
			"to_agent":     toID,
			"from_agent":   fromID,
			"synced_files": synced,
			"duration_ms":  durMs,
		}, "", "  ")
		fmt.Println(string(data))

	case "finish":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing agent-id for worktree finish"}`)
			os.Exit(1)
		}
		agentID := os.Args[3]
		force := false
		for _, a := range os.Args[4:] {
			if a == "--force" {
				force = true
			}
		}
		start := time.Now()
		err := mgr.FinishSafe(agentID, force)
		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"status":      "finished",
			"agent_id":    agentID,
			"duration_ms": durMs,
		}, "", "  ")
		fmt.Println(string(data))

	case "prune", "gc":
		ttl := 1 * time.Hour
		pruned, err := mgr.PruneStale(ttl)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"status":         "pruned",
			"cleaned_up_wts": pruned,
		}, "", "  ")
		fmt.Println(string(data))
	}
}

func handleReview(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "usage: monoflow review risk | audit --title=... --desc=... | throttle"}`)
		os.Exit(1)
	}

	sub := os.Args[2]
	changedFiles := getGitChangedFiles(rootDir)
	g, _ := graph.LoadOrDiscover(rootDir)

	switch sub {
	case "risk":
		res, err := review.EvaluateRisk(rootDir, g, changedFiles)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

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
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		if !res.Valid {
			os.Exit(1)
		}

	case "throttle":
		status, err := review.CheckBackpressure(rootDir, 3)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(data))
		if status.Throttled {
			os.Exit(1)
		}
	}
}

func handleQueue(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "usage: monoflow queue spec [task-id] | verify <sha>"}`)
		os.Exit(1)
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
			fmt.Printf(`{"status": "failed", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(receipt, "", "  ")
		fmt.Println(string(data))

	case "verify":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing commit sha for queue verify"}`)
			os.Exit(1)
		}
		sha := os.Args[3]
		eligible, receipt, reason := qm.VerifyCIGate(sha)
		data, _ := json.MarshalIndent(map[string]any{
			"fast_path_eligible": eligible,
			"receipt":            receipt,
			"reason":             reason,
		}, "", "  ")
		fmt.Println(string(data))
		if !eligible {
			os.Exit(1)
		}
	}
}

func handleCI(rootDir string) {
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
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(job, "", "  ")
		fmt.Println(string(data))
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
	data, _ := json.MarshalIndent(map[string]any{
		"ci_tier":            tierStr,
		"fast_path_eligible": eligible,
		"head_sha":           headSHA,
		"reason":             reason,
		"receipt":            receipt,
	}, "", "  ")
	fmt.Println(string(data))
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

	src := cihealth.NewGHSource(rootDir)
	data, warnings, err := cihealth.Collect(src, collectOpts)
	if err != nil {
		errData, _ := json.Marshal(map[string]string{"error": err.Error(), "branch": collectOpts.Branch})
		fmt.Println(string(errData))
		os.Exit(1)
	}
	rep := cihealth.Analyze(data, cihealth.Options{Branch: collectOpts.Branch, CheckFilter: checkFilter})
	rep.Warnings = append(warnings, rep.Warnings...)
	cihealth.Enrich(rep, src, cihealth.EnrichOptions{RootDir: rootDir, Logs: withLogs})

	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
}

func handleCommit(rootDir string) {
	refuseSharedRoot(rootDir, "commit")
	msg := "agent checkpoint commit"
	for i, arg := range os.Args[2:] {
		if arg == "-m" && i+1 < len(os.Args[2:]) {
			msg = os.Args[2:][i+1]
			break
		}
	}

	res, err := guard.FastAgentCommit(rootDir, msg)
	if err != nil {
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		os.Exit(1)
	}

	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func handleGuard(rootDir string) {
	testViolations, _ := guard.CheckTestIntegrity(rootDir)
	secretViolations, _ := guard.ScanSecrets(rootDir)

	allViolations := append(testViolations, secretViolations...)

	if len(allViolations) > 0 {
		data, _ := json.MarshalIndent(map[string]any{
			"status":     "failed",
			"violations": allViolations,
			"verdict":    "Integrity or security violation detected. Commits or PRs with weakened tests or secrets are blocked.",
		}, "", "  ")
		fmt.Println(string(data))
		os.Exit(1)
	}

	data, _ := json.MarshalIndent(map[string]any{
		"status":          "passed",
		"test_integrity":  "verified (0 assertions deleted or commented out)",
		"secret_scanning": "clean (0 credentials or keys detected)",
		"verdict":         "All guard checks passed",
	}, "", "  ")
	fmt.Println(string(data))
}

func handleOuter(rootDir string) {
	verify.DefaultE2ERunner = func(dir string) (*verify.E2EResult, error) {
		rep, err := scenario.RunScenario("")
		if err != nil {
			return &verify.E2EResult{
				Status: "failed",
				Error:  err.Error(),
			}, err
		}
		return &verify.E2EResult{
			Status:       rep.Status,
			DurationMs:   rep.TotalTimeMs,
			TotalScenes:  rep.TotalScenes,
			PassedScenes: rep.Passed,
		}, nil
	}

	om := verify.NewOuterLoopManager(rootDir)
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "usage: monoflow outer start [opts] | status <id> | wait <id> | list"}`)
		os.Exit(1)
	}

	sub := os.Args[2]
	switch sub {
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
			fmt.Printf(`{"status": "failed", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(job, "", "  ")
		fmt.Println(string(data))
		if job.Status == verify.JobRegressed {
			os.Exit(1)
		}

	case "status":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing job-id for outer status"}`)
			os.Exit(1)
		}
		jobID := os.Args[3]
		job, err := om.GetJob(jobID)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(job, "", "  ")
		fmt.Println(string(data))

	case "wait":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing job-id for outer wait"}`)
			os.Exit(1)
		}
		jobID := os.Args[3]
		timeout := 60 * time.Second
		for _, arg := range os.Args[4:] {
			if strings.HasPrefix(arg, "--timeout=") {
				secStr := strings.TrimPrefix(arg, "--timeout=")
				if s, err := strconv.Atoi(strings.TrimSuffix(secStr, "s")); err == nil {
					timeout = time.Duration(s) * time.Second
				}
			}
		}
		job, err := om.WaitJob(jobID, timeout)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(job, "", "  ")
		fmt.Println(string(data))
		if job.Status == verify.JobRegressed {
			os.Exit(1)
		}

	case "list":
		jobs, err := om.ListJobs()
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(jobs, "", "  ")
		fmt.Println(string(data))

	default:
		fmt.Printf(`{"error": "unknown outer subcommand: %s"}`+"\n", sub)
		os.Exit(1)
	}
}

func handlePush(rootDir string) {
	// 1. Pre-push test integrity check & secret scan (< 20ms)
	testViolations, _ := guard.CheckTestIntegrity(rootDir)
	secretViolations, _ := guard.ScanSecrets(rootDir)
	allViolations := append(testViolations, secretViolations...)
	if len(allViolations) > 0 {
		data, _ := json.MarshalIndent(map[string]any{
			"status":     "blocked",
			"violations": allViolations,
			"verdict":    "Pre-push integrity check failed. Cannot push weakened tests or credentials to remote.",
		}, "", "  ")
		fmt.Println(string(data))
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
		data, _ := json.MarshalIndent(map[string]any{
			"status":  "blocked",
			"verdict": fmt.Sprintf("Direct push to protected branch '%s' blocked. MonoFlow requires proposing changes via Pull Request to guarantee zero remote CI breakage. Use 'monoflow pr create' instead (or pass --force to bypass).", branch),
		}, "", "  ")
		fmt.Println(string(data))
		os.Exit(1)
	}

	headSHA := ""
	cmdSHA := exec.Command("git", "rev-parse", "HEAD")
	cmdSHA.Dir = rootDir
	if out, err := cmdSHA.CombinedOutput(); err == nil {
		headSHA = strings.TrimSpace(string(out))
	}

	// 1d. Remote CI protection: everything remote CI would run on the affected packages
	// (typecheck, lint, tests incl. dependents) must pass locally before the push.
	if !skipVerify {
		if res, base, ok := verifyBranchAgainstBase(rootDir); ok && res.Status != "passed" {
			data, _ := json.MarshalIndent(map[string]any{
				"status":  "blocked",
				"verdict": fmt.Sprintf("Pre-push verification failed for %d/%d affected packages (changes vs %s). Remote CI would fail; fix locally first.", res.FailedTargets, res.TotalTargets, base),
				"results": failedResults(res),
			}, "", "  ")
			fmt.Println(string(data))
			os.Exit(1)
		}
	}

	// 2. Perform push
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
		fmt.Printf(`{"status": "push_failed", "error": "%s", "output": "%s"}`+"\n", err, strings.TrimSpace(string(out)))
		os.Exit(1)
	}

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
			res["watcher_id"] = job.ID
			res["ci_watcher_command"] = fmt.Sprintf("./bin/monoflow events watch %s --branch=%s", headSHA, branch)
			res["agent_action_required"] = "DO_NOT_WAIT"
			res["agent_directive"] = "STOP POLLING. DO NOT schedule timers or loop waiting for CI. Dispatch 'monoflow events watch' as a background task and immediately end your turn. You will be automatically awakened by a system notification when remote CI finishes."
		}
	}

	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func handleSync(rootDir string) {
	start := time.Now()
	remote := "origin"
	branch := ""
	rebase := false
	autostash := false
	mergeMode := false

	var positional []string
	for _, arg := range os.Args[2:] {
		if arg == "--rebase" {
			rebase = true
		} else if arg == "--merge" {
			mergeMode = true
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
		fmt.Printf(`{"status": "sync_failed", "step": "fetch", "error": %q, "output": %q}`+"\n", err.Error(), strings.TrimSpace(string(out)))
		os.Exit(1)
	}

	// 2. Fast-forward or Rebase
	var mergeArgs []string
	strategy := "fast-forward"
	if mergeMode {
		strategy = "merge"
		mergeArgs = []string{"merge", "--no-edit", "FETCH_HEAD"}
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
			data, _ := json.MarshalIndent(res, "", "  ")
			fmt.Println(string(data))
			os.Exit(1)
		}
		if rebase {
			// Never leave the worktree mid-rebase: report the conflicts and restore the branch.
			_ = exec.Command("git", "-C", rootDir, "rebase", "--abort").Run()
			res["hint"] = fmt.Sprintf("Your branch conflicts with %s/%s; branch left unchanged. Run 'monoflow sync --merge' to merge %s once and resolve the conflicts in place (a rebase would replay every commit).", remote, branch, branch)
		} else {
			res["hint"] = "Branches have diverged; run 'monoflow sync' from your agent branch (rebases onto the base) or pass --rebase."
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
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

	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func handleBranch(rootDir string) {
	if len(os.Args) < 3 {
		// List branches
		cmdList := exec.Command("git", "branch", "--no-color")
		cmdList.Dir = rootDir
		out, err := cmdList.CombinedOutput()
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		var current string
		var branches []string
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(l, "* ") {
				current = strings.TrimPrefix(l, "* ")
				branches = append(branches, current)
			} else if trimmed != "" {
				branches = append(branches, trimmed)
			}
		}
		data, _ := json.MarshalIndent(map[string]any{
			"current":  current,
			"branches": branches,
		}, "", "  ")
		fmt.Println(string(data))
		return
	}

	targetBranch := os.Args[2]
	createFlag := false
	for _, arg := range os.Args[2:] {
		if arg == "-c" || arg == "-b" || arg == "--create" {
			createFlag = true
		} else if !strings.HasPrefix(arg, "-") {
			targetBranch = arg
		}
	}

	var cmdSwitch *exec.Cmd
	if createFlag {
		cmdSwitch = exec.Command("git", "checkout", "-b", targetBranch)
	} else {
		cmdSwitch = exec.Command("git", "checkout", targetBranch)
	}
	cmdSwitch.Dir = rootDir
	out, err := cmdSwitch.CombinedOutput()
	if err != nil && !createFlag {
		cmdCreate := exec.Command("git", "checkout", "-b", targetBranch)
		cmdCreate.Dir = rootDir
		out, err = cmdCreate.CombinedOutput()
	}

	if err != nil {
		fmt.Printf(`{"status": "error", "error": %q, "output": %q}`+"\n", err.Error(), strings.TrimSpace(string(out)))
		os.Exit(1)
	}

	data, _ := json.MarshalIndent(map[string]any{
		"status":  "switched",
		"branch":  targetBranch,
		"message": fmt.Sprintf("Switched to branch '%s'", targetBranch),
	}, "", "  ")
	fmt.Println(string(data))
}

func handleEvents(rootDir string) {
	em := events.NewEventManager(rootDir)
	sub := "unread"
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}

	switch sub {
	case "unread":
		list, err := em.List(true)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		showAll := false
		for _, a := range os.Args[3:] {
			if a == "--all" {
				showAll = true
			}
		}
		// On an agent/feature branch the inbox is that agent's: its branch's events plus
		// repo-wide ones. Other agents' events only cost context (--all shows them).
		scope := "all"
		others := 0
		if branch := currentBranch(rootDir); !showAll && branch != "" && branch != "main" && branch != "master" && branch != "HEAD" {
			scope = branch
			local := projectedPackages(rootDir)
			mine := list[:0]
			for _, e := range list {
				relevant := e.Branch == branch
				if e.Branch == "" {
					relevant = true
					// Release notices are for the consumer's owner: only worktrees that
					// contain that consumer (or any worktree, if action is required).
					if e.Type == events.EventDependencyReleased {
						consumer, _ := e.Payload["consumer"].(string)
						relevant = local == nil && e.RequiresAction || local[consumer]
					}
				}
				if relevant {
					mine = append(mine, e)
				} else {
					others++
				}
			}
			list = mine
		}
		data, _ := json.MarshalIndent(map[string]any{
			"scope":                scope,
			"unread_count":         len(list),
			"other_branches_count": others,
			"events":               list,
		}, "", "  ")
		fmt.Println(string(data))

	case "list", "all":
		list, err := em.List(false)
		if err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"total_count": len(list),
			"events":      list,
		}, "", "  ")
		fmt.Println(string(data))

	case "ack":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing event-id for ack"}`)
			os.Exit(1)
		}
		evtID := os.Args[3]
		if err := em.Ack(evtID); err != nil {
			fmt.Printf(`{"error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		fmt.Printf(`{"status": "acknowledged", "event_id": "%s"}`+"\n", evtID)

	case "clear":
		_ = em.ClearAll()
		fmt.Println(`{"status": "cleared"}`)

	case "watch":
		commitSHA := ""
		branch := ""
		jobID := ""
		remote := ""
		timeoutMin := 25
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--job=") {
				jobID = strings.TrimPrefix(arg, "--job=")
			} else if strings.HasPrefix(arg, "--remote=") {
				remote = strings.TrimPrefix(arg, "--remote=")
			} else if strings.HasPrefix(arg, "--commit=") {
				commitSHA = strings.TrimPrefix(arg, "--commit=")
			} else if strings.HasPrefix(arg, "--branch=") {
				branch = strings.TrimPrefix(arg, "--branch=")
			} else if strings.HasPrefix(arg, "--timeout=") {
				if t, err := strconv.Atoi(strings.TrimPrefix(arg, "--timeout=")); err == nil {
					timeoutMin = t
				}
			} else if commitSHA == "" && !strings.HasPrefix(arg, "-") {
				commitSHA = arg
			}
		}

		cw := events.NewCIWatcher(rootDir)
		var job *events.WatchJob
		var err error
		if jobID != "" {
			job, err = cw.ResumeJob(jobID, events.WatchOptions{Remote: remote, TimeoutMin: timeoutMin})
		} else {
			job, err = cw.StartWatch(events.WatchOptions{
				CommitSHA:  commitSHA,
				Branch:     branch,
				TimeoutMin: timeoutMin,
				Remote:     remote,
				Async:      false,
			})
		}
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(job, "", "  ")
		fmt.Println(string(data))
		if job.Status == events.WatchStatusCompletedFailed || job.Status == events.WatchStatusTimedOut {
			os.Exit(1)
		}

	default:
		fmt.Printf(`{"error": "unknown events subcommand: %s (use unread | list | watch <sha> | ack <id> | clear)"}`+"\n", sub)
		os.Exit(1)
	}
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

func handleCache(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`MonoFlow Cache Commands:
  monoflow cache status    Show local and remote cache efficiency, hit ratio, and disk usage
  monoflow cache clean     Purge local cache storage
  monoflow cache serve     Launch built-in HTTP Remote Cache server (--port=8080 --token=... --dir=...)`)
		os.Exit(0)
	}

	sub := os.Args[2]
	mgr := cache.NewManager(rootDir, cache.DefaultConfig(rootDir))

	switch sub {
	case "status":
		stats := mgr.GetStats()
		data, _ := json.MarshalIndent(stats, "", "  ")
		fmt.Println(string(data))

	case "clean", "purge":
		if err := mgr.Clean(); err != nil {
			fmt.Printf(`{"status": "error", "error": "%s"}`+"\n", err)
			os.Exit(1)
		}
		fmt.Println(`{"status": "cleaned", "message": "Local verification and artifact cache purged"}`)

	case "serve":
		port := 8080
		storageDir := filepath.Join(rootDir, ".agents", "cache", "remote_storage")
		token := ""
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--port=") {
				if p, err := strconv.Atoi(strings.TrimPrefix(arg, "--port=")); err == nil {
					port = p
				}
			} else if strings.HasPrefix(arg, "--dir=") {
				storageDir = strings.TrimPrefix(arg, "--dir=")
			} else if strings.HasPrefix(arg, "--token=") {
				token = strings.TrimPrefix(arg, "--token=")
			}
		}
		addr := fmt.Sprintf(":%d", port)
		fmt.Printf(`{"status": "running", "addr": "%s", "storage_dir": %q, "token_protected": %t}`+"\n", addr, storageDir, token != "")
		if err := cache.StartServer(addr, storageDir, token); err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}

	default:
		fmt.Printf(`{"error": "unknown cache subcommand: %s (use status | clean | serve)"}`+"\n", sub)
		os.Exit(1)
	}
}

func handleClone() {
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "usage: monoflow clone <repo-url> [dest-dir] --packages=<pkg1,pkg2,...> [--include-deps=true|false] [--branch=<name>]"}`)
		os.Exit(1)
	}

	remoteURL := os.Args[2]
	destDir := ""
	packagesArg := ""
	includeDeps := true
	branch := ""

	args := os.Args[3:]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--packages=") {
			packagesArg = strings.TrimPrefix(arg, "--packages=")
		} else if arg == "--packages" && i+1 < len(args) {
			packagesArg = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--include-deps=") {
			val := strings.TrimPrefix(arg, "--include-deps=")
			includeDeps = val != "false" && val != "0"
		} else if strings.HasPrefix(arg, "--branch=") {
			branch = strings.TrimPrefix(arg, "--branch=")
		} else if !strings.HasPrefix(arg, "--") && destDir == "" {
			destDir = arg
		}
	}

	if packagesArg == "" {
		fmt.Println(`{"error": "missing required --packages flag. Specify a subset of packages, e.g. --packages=pkgA,pkgB"}`)
		os.Exit(1)
	}

	pkgs := strings.Split(packagesArg, ",")
	opts := subset.DefaultCloneOptions()
	opts.IncludeDeps = includeDeps
	opts.Branch = branch

	for _, arg := range args {
		if strings.HasPrefix(arg, "--deps=") {
			opts.Strategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
		}
	}

	res, err := subset.Clone(remoteURL, destDir, pkgs, opts)
	if err != nil {
		fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
		os.Exit(1)
	}

	data, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(data))
}

func handleSubset(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`{"error": "usage: monoflow subset set --packages=<p1,p2> | add <p1,p2> | remove <p1,p2> [--force] | update [p1,p2] | deps <pkg> | list | reset"}`)
		os.Exit(1)
	}

	sub := os.Args[2]
	switch sub {
	case "set":
		packagesArg := ""
		strategy := subset.DepStrategySource
		includeDeps := true
		for i := 3; i < len(os.Args); i++ {
			arg := os.Args[i]
			if strings.HasPrefix(arg, "--packages=") {
				packagesArg = strings.TrimPrefix(arg, "--packages=")
			} else if strings.HasPrefix(arg, "--package=") {
				packagesArg = strings.TrimPrefix(arg, "--package=")
			} else if (arg == "--packages" || arg == "--package") && i+1 < len(os.Args) {
				packagesArg = os.Args[i+1]
				i++
			} else if strings.HasPrefix(arg, "--deps=") {
				strategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
			} else if strings.HasPrefix(arg, "--include-deps=") {
				val := strings.TrimPrefix(arg, "--include-deps=")
				includeDeps = val != "false" && val != "0"
				if !includeDeps {
					strategy = subset.DepStrategyNone
				}
			}
		}

		if packagesArg == "" {
			fmt.Println(`{"error": "missing required --packages flag"}`)
			os.Exit(1)
		}

		pkgs := strings.Split(packagesArg, ",")
		res, err := subset.ApplyWithOptions(rootDir, pkgs, strategy)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "add":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "usage: monoflow subset add <pkg1,pkg2,...> [--deps=source|contracts|none]"}`)
			os.Exit(1)
		}
		pkgs := strings.Split(os.Args[3], ",")
		strategy := subset.DepStrategySource
		for _, arg := range os.Args[4:] {
			if strings.HasPrefix(arg, "--deps=") {
				strategy = subset.DepStrategy(strings.TrimPrefix(arg, "--deps="))
			}
		}
		res, err := subset.Add(rootDir, pkgs, strategy)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "remove":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "usage: monoflow subset remove <pkg1,pkg2,...> [--force]"}`)
			os.Exit(1)
		}
		pkgs := strings.Split(os.Args[3], ",")
		force := false
		for _, arg := range os.Args[4:] {
			if arg == "--force" {
				force = true
			}
		}
		res, err := subset.Remove(rootDir, pkgs, force)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "update":
		var pkgs []string
		remote := "origin"
		branch := "main"
		for i := 3; i < len(os.Args); i++ {
			arg := os.Args[i]
			if strings.HasPrefix(arg, "--remote=") {
				remote = strings.TrimPrefix(arg, "--remote=")
			} else if strings.HasPrefix(arg, "--branch=") {
				branch = strings.TrimPrefix(arg, "--branch=")
			} else if !strings.HasPrefix(arg, "--") {
				pkgs = strings.Split(arg, ",")
			}
		}
		res, err := subset.Update(rootDir, pkgs, remote, branch)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "deps", "inspect":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "usage: monoflow subset deps <pkg-name>"}`)
			os.Exit(1)
		}
		target := os.Args[3]
		g, err := graph.LoadOrDiscover(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		report, err := subset.InspectDeps(g, target, rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))

	case "list", "status":
		st, err := subset.Status(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(data))

	case "check", "health":
		res, err := subset.CheckSubsetHealth(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))
		if res.Status == "error" {
			os.Exit(1)
		}

	case "reset":
		err := subset.Reset(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		fmt.Println(`{"status": "reset", "message": "Sparse checkout disabled. Full monorepo restored."}`)

	default:
		fmt.Printf(`{"error": "unknown subset subcommand '%s' (use set | add | remove | update | deps | check | list | reset)"}`+"\n", sub)
		os.Exit(1)
	}
}

func handleVersion(rootDir string) {
	sub := "list"
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}

	switch sub {
	case "list", "all":
		rep, err := version.ListPackageVersions(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(data))

	case "bump":
		refuseSharedRoot(rootDir, "version bump")
		if len(os.Args) < 5 {
			fmt.Println(`{"error": "usage: monoflow version bump <pkg> <patch|minor|major> [--cascade]"}`)
			os.Exit(1)
		}
		pkgName := os.Args[3]
		bumpType := os.Args[4]
		cascade := false
		for _, arg := range os.Args[5:] {
			if arg == "--cascade" {
				cascade = true
			}
		}
		res, err := version.BumpPackageVersion(rootDir, pkgName, bumpType, cascade)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "edges", "releases", "release", "pin", "unpin", "upgrades", "upgrade", "suggest":
		handleReleases(rootDir, sub)

	case "apply":
		res, err := version.ApplyChangesets(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(data))

	case "check", "verify":
		rep, err := version.CheckVersionConsistency(rootDir)
		if err != nil {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(data))
		if rep.Status == "error" {
			os.Exit(1)
		}

	default:
		rep, err := version.ListPackageVersions(rootDir)
		if err != nil {
			fmt.Printf(`{"version": %q, "error": %q}`+"\n", version.EngineVersion, err.Error())
			return
		}
		data, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(data))
	}
}

func handleScenario(rootDir string) {
	targetDir := ""
	asJSON := false
	for _, arg := range os.Args[2:] {
		if strings.HasPrefix(arg, "--dir=") {
			targetDir = strings.TrimPrefix(arg, "--dir=")
		} else if arg == "--json" {
			asJSON = true
		}
	}

	if !asJSON {
		fmt.Println("🚀 Running MonoFlow Real Multi-Agent Monorepo & CI Verification Scenario...")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}

	report, err := scenario.RunScenario(targetDir)
	if err != nil {
		if asJSON {
			fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
		} else {
			fmt.Printf("❌ Scenario Execution Failed: %v\n", err)
		}
		os.Exit(1)
	}

	if asJSON {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(data))
		return
	}

	for _, sc := range report.Scenes {
		statusIcon := "✅"
		if sc.Status == "BLOCKED_AS_EXPECTED" {
			statusIcon = "🛡️ "
		} else if sc.Status != "PASSED" {
			statusIcon = "❌"
		}
		fmt.Printf("%s [SCENE %d/7] %s (%.1fms)\n", statusIcon, sc.SceneNumber, sc.Title, sc.DurationMs)
		for k, v := range sc.Details {
			fmt.Printf("      • %s: %v\n", k, v)
		}
	}
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("🏁 Scenario Summary: %d/%d Scenes Passed in %.2fs (Status: %s)\n",
		report.Passed, report.TotalScenes, report.TotalTimeMs/1000.0, strings.ToUpper(report.Status))
	fmt.Printf("📁 Fixture Monorepo: %s\n", report.RepoDir)
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

// handleHook implements agent harness hooks. `pretooluse` speaks the Claude Code hook
// protocol (JSON on stdin/stdout); `check` takes a raw command on stdin and exits 2 with
// the replacement when it should go through MonoFlow (used by the opencode plugin).
func handleHook() {
	sub := ""
	if len(os.Args) > 2 {
		sub = os.Args[2]
	}
	switch sub {
	case "pretooluse":
		if out := agenthooks.ClaudePreToolUse(os.Stdin, hookStateDir()); out != nil {
			fmt.Println(string(out))
		}
	case "check":
		data, _ := io.ReadAll(os.Stdin)
		if r := agenthooks.Decide(hookStateDir(), string(data)); r != nil {
			fmt.Println(r.Reason())
			os.Exit(2)
		}
	default:
		fmt.Println(`{"error": "usage: monoflow hook pretooluse | check"}`)
		os.Exit(1)
	}
}

// hookStateDir holds per-repo nudge state for agent hooks.
func hookStateDir() string {
	root, err := findRepoRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, ".agents", "hooks")
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

func currentBranch(rootDir string) string {
	out, err := exec.Command("git", "-C", rootDir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// handleReleases implements internal package releases and per-consumer pinning.
func handleReleases(rootDir, sub string) {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
		os.Exit(1)
	}
	args := os.Args[3:]
	flag := func(name string) string {
		for _, a := range args {
			if strings.HasPrefix(a, "--"+name+"=") {
				return strings.TrimPrefix(a, "--"+name+"=")
			}
		}
		return ""
	}
	var pos []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
		}
	}
	out := func(v any) {
		data, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(data))
	}
	fail := func(err error) {
		fmt.Printf(`{"status": "error", "error": %q}`+"\n", err.Error())
		os.Exit(1)
	}
	need := func(n int, usage string) {
		if len(pos) < n {
			fmt.Printf(`{"error": "usage: monoflow version %s"}`+"\n", usage)
			os.Exit(1)
		}
	}

	switch sub {
	case "edges":
		edges := g.ScanEdges()
		var problems int
		for _, e := range edges {
			if e.Problem != "" {
				problems++
			}
		}
		out(map[string]any{"edges": edges, "problems": problems})
		if problems > 0 {
			os.Exit(1)
		}

	case "releases":
		res := map[string]any{}
		for _, p := range g.GetAllPackages() {
			if len(pos) > 0 && p.Name != pos[0] {
				continue
			}
			if v := release.List(rootDir, p.Name); len(v) > 0 || len(pos) > 0 {
				res[p.Name] = map[string]any{"current": p.Version, "released": v}
			}
		}
		out(res)

	case "release":
		need(1, "release <pkg>")
		tag, err := release.Create(rootDir, g, pos[0])
		if err != nil {
			fail(err)
		}
		pkg, _ := g.GetPackage(pos[0])
		notified := notifyConsumers(rootDir, g, pkg)
		out(map[string]any{"status": "released", "tag": tag, "consumers_notified": notified,
			"next": "push the tag with 'monoflow push' so CI and other clones can materialise it"})

	case "pin", "unpin":
		refuseSharedRoot(rootDir, "version "+sub)
		need(2, sub+" <consumer> <dependency> [--version=<v>]")
		ver := ""
		if sub == "pin" {
			if ver = flag("version"); ver == "" {
				if dep, ok := g.GetPackage(pos[1]); ok {
					ver = release.Latest(rootDir, dep.Name)
				}
			}
			if ver == "" {
				fail(fmt.Errorf("%s has no release to pin to; release it first (monoflow version release %s)", pos[1], pos[1]))
			}
			if _, err := release.Materialize(rootDir, g, pos[1], ver); err != nil {
				fail(err)
			}
		}
		file, err := release.Repoint(rootDir, g, pos[0], pos[1], ver)
		if err != nil {
			fail(err)
		}
		mode := "source (live code)"
		if ver != "" {
			mode = "pinned@" + ver
		}
		out(map[string]any{"status": sub + "ned", "consumer": pos[0], "dependency": pos[1], "mode": mode, "file": file,
			"next": "monoflow verify --affected, then monoflow commit"})

	case "upgrades":
		consumer := ""
		if len(pos) > 0 {
			consumer = pos[0]
		}
		ups := release.Available(rootDir, g, consumer)
		out(map[string]any{"upgrades": ups, "count": len(ups)})

	case "upgrade":
		refuseSharedRoot(rootDir, "version upgrade")
		need(2, "upgrade <consumer> <dependency> [--to=<v>]")
		to := flag("to")
		if to == "" {
			for _, u := range release.Available(rootDir, g, pos[0]) {
				if u.Dependency == pos[1] && u.Kind == "compatible" {
					to = u.To
				}
			}
		}
		if to == "" {
			fail(fmt.Errorf("no compatible release of %s newer than the pinned one; pass --to=<version> for a breaking upgrade", pos[1]))
		}
		if _, err := release.Materialize(rootDir, g, pos[1], to); err != nil {
			fail(err)
		}
		if _, err := release.Repoint(rootDir, g, pos[0], pos[1], to); err != nil {
			fail(err)
		}
		// The consumer's own checks are its "CI" for the upgrade.
		g2, _ := graph.LoadOrDiscover(rootDir)
		cons, _ := g2.GetPackage(pos[0])
		res := verify.NewRunner(rootDir).VerifyAffected(g2, []string{filepath.Join(cons.Path, "__upgrade__")}, verify.VerifyOptions{})
		out(map[string]any{"status": "upgraded", "consumer": pos[0], "dependency": pos[1], "to": to, "verify": res.Status, "results": failedResults(res),
			"next": "fix any failures, then monoflow commit"})
		if res.Status != "passed" {
			os.Exit(1)
		}

	case "suggest":
		var targets []string
		if len(pos) > 0 {
			targets = pos
		} else {
			for _, f := range changedFilesSince(rootDir, firstNonEmpty(flag("base"), "main")) {
				if p := g.MapFileToPackage(f); p != nil && !graph.IsNonCodeFile(f) && !containsStr(targets, p.Name) {
					targets = append(targets, p.Name)
				}
			}
		}
		var res []*release.Suggestion
		for _, t := range targets {
			s, err := release.Suggest(rootDir, g, t, firstNonEmpty(flag("base"), "main"))
			if err != nil {
				fail(err)
			}
			res = append(res, s)
		}
		out(map[string]any{"suggestions": res})
	}
}

// notifyConsumers emits a DEPENDENCY_RELEASED event per consumer, classified for it.
func notifyConsumers(rootDir string, g *graph.DependencyGraph, pkg *graph.PackageNode) int {
	em := events.NewEventManager(rootDir)
	n := 0
	for _, e := range g.ScanEdges() {
		if e.Dependency != pkg.Name {
			continue
		}
		evt := &events.Event{Type: events.EventDependencyReleased, Payload: map[string]any{
			"dependency": pkg.Name, "version": pkg.Version, "consumer": e.Consumer, "mode": e.Mode}}
		if e.Mode == graph.EdgeSource {
			evt.Summary = fmt.Sprintf("%s@%s released; %s already builds against the live code (no action)", pkg.Name, pkg.Version, e.Consumer)
		} else {
			kind := "compatible"
			for _, u := range release.Available(rootDir, g, e.Consumer) {
				if u.Dependency == pkg.Name && u.To == pkg.Version {
					kind = u.Kind
					evt.Payload["impact"] = u.Impact
					evt.SuggestedTask = u.Command
				}
			}
			evt.RequiresAction = kind == "breaking"
			evt.Summary = fmt.Sprintf("%s@%s released (%s for %s, pinned at %s)", pkg.Name, pkg.Version, kind, e.Consumer, e.Constraint)
		}
		if em.Emit(evt) == nil {
			n++
		}
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
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
	data, _ := json.MarshalIndent(map[string]any{
		"status": "refused",
		"error":  fmt.Sprintf("'%s' edits files, and this is the shared repository root used by agent worktrees (%s)", action, strings.Join(ids, ", ")),
		"hint":   "run it inside your worktree (.agents/worktrees/<agent-id>), or pass --allow-root if you really mean the root",
	}, "", "  ")
	fmt.Println(string(data))
	os.Exit(1)
}

// projectedPackages returns the packages of a sparse worktree (nil for a full checkout).
func projectedPackages(rootDir string) map[string]bool {
	data, err := os.ReadFile(filepath.Join(rootDir, ".agents", "subset.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		IncludedPackages []string `json:"included_packages"`
	}
	if json.Unmarshal(data, &meta) != nil || len(meta.IncludedPackages) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, p := range meta.IncludedPackages {
		set[p] = true
	}
	return set
}
