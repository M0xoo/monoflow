package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/antigravity/monoflow/pkg/telemetry"
)

func main() {
	_ = os.Setenv("MONOFLOW_INTERNAL", "1")
	takeJSONFlag()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	maybeWarmBaseline(cmd)
	if isHelpArg(cmd) {
		switch {
		case len(os.Args) > 2 && os.Args[2] == "--all":
			printUsage()
		case len(os.Args) > 2:
			printCommandUsage(os.Args[2])
		default:
			printShortUsage()
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

	case "baseline-warm": // internal: see maybeWarmBaseline
		warmBaseline(rootDir)

	case "speculate-watch": // internal: started by start, see speculate.go
		speculateWatch(rootDir)

	case "start":
		handleStart(rootDir)
	case "ship":
		handleShip(rootDir)
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

// printShortUsage is what `monoflow help` prints: the agent path and command names. The full
// list (3 KB) is behind `help --all`; agents read help early and carry it in context all run.
func printShortUsage() {
	fmt.Println(`MonoFlow. Agent path (4 calls):
  monoflow start <task-id>      worktree + branch; cd into the printed path
  monoflow verify               build+lint+test changed packages and every dependent (= CI for this change)
  monoflow ship -m "<msg>"      commit, local CI on that commit, push, open the PR

More: graph (query|impact) · checkpoint · commit · push · sync · events · ci · outer · worktree ·
pr · review · queue · quarantine · version · task · guard · subset · cache · deliver · init · hook
  monoflow help <command>       flags of one command      monoflow help --all   everything`)
}

func usageText(w io.Writer) {
	fmt.Fprintln(w, `MonoFlow — The Agent-Native Monorepo & Ultra-Fast CI Engine

Agent path (4 calls):
  monoflow start <task-id>            Isolated worktree on branch agent/<task-id>; then cd into its path
  monoflow verify                     Build+lint+test changed packages and every dependent (= CI for this change)
  monoflow ship -m "<msg>" [--title=] Commit, local CI on that commit, push, open the PR

Usage:
  monoflow init [dir] [--setup-gh-actions]  Install MonoFlow (+ economical affected-only GitHub Actions CI)
  monoflow clone <url> [dest] --packages=<p1,p2> Partial clone of selected packages and their dependencies
  monoflow subset [set|add|remove|update|deps|check|status|reset] Manage package subset projection & dependency health
  monoflow graph [query|impact]       Query upstream/downstream dependencies & impact
  monoflow checkpoint [save|revert]   Snapshot / restore the working tree (~50-100ms, outside git history)
  monoflow worktree [create|sync|gc]  Per-agent worktrees on agent/<id> branches, sparse package projection
  monoflow verify [--affected|--proof] Typecheck+lint+test changed packages and dependents (cached; text, --json for scripts)
  monoflow outer [start|status|wait]  Async outer loop: transitive matrix & remote promotion barrier
  monoflow push [remote] [branch]     Fire-and-forget push with background autonomous CI watcher
  monoflow sync [remote] [branch]     Fast remote sync (ff-only or rebase) keeping sparse subset
  monoflow branch [name]              List, create, or switch branches without raw git commands
  monoflow events [unread|list|ack]   Event inbox (CI, outer loop, releases); delivered to agents by harness hooks
  monoflow review [risk|audit|throttle] Machine risk scoring, anti-hallucination & backpressure
  monoflow queue [spec|verify]        Speculative local merge queue with flaky quarantine
  monoflow ci [check|watch|health]    Two-tier CI gate, CI watcher & CI health history (when/which commit broke a check)
  monoflow commit -m "<msg>"          Guarded commit (no weakened tests/secrets); starts the outer loop
  monoflow guard [check]              Test integrity guard (anti-cheating) & secret scanner
  monoflow task [check|prove|slice]   DoD contracts, Proof of Work receipts, & stacked diffs
  monoflow deliver [--target=<p>]     Assemble hermetic deliverable artifact (.agents/dist)
  monoflow cache [status|clean|serve] Local & remote package cache metrics, hit ratio & built-in HTTP daemon
  monoflow scenario [run] [--json]    Real end-to-end multi-agent enterprise monorepo simulation
  monoflow app test                   Delta compilation + symlink overlay for whole-app tests
  monoflow pr [create|list|view|status|review|test|local|merge] Full agent-native PR suite with affected packages
  monoflow quarantine [list|add]      Manage flaky test quarantine
  monoflow version [list|bump|apply|check] SemVer package versioning, changesets & cascade
  monoflow hook [pretooluse|check|deliver|stop] Agent harness hooks: redirect raw commands, deliver events, background verify
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
	"start": `  start <task-id>                     Isolated worktree on branch agent/<task-id> off the latest main (full checkout)
  start <task-id> --packages=<p1,p2>  Same, projecting only those packages (+ their dependencies)
  start <task-id> --from-local        Start from your local main instead of origin's (the default)
Then cd into the printed path and work there.`,
	"ship": `  ship -m "<commit message>"          Commit uncommitted work, run local CI on that commit, push, open the PR
  ship -m "<msg>" --title="<PR title>" --body="<text>" | --body-file=<path>
One call replaces commit + pr create. Afterwards end your turn: CI results are delivered to you.`,
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
  verify --update-snapshots | -u      Rewrite mismatched vitest/jest snapshots (when the new values are intended)
  verify --base=<ref>                 Changes since the merge-base with <ref> (CI: the PR base)
  verify --strict                     Also block on failures that pre-exist on the base branch
  verify --no-fix                     Do not run the packages' fix_cmd (formatter) on your changed files first
  verify --proof=<task-id>            Verify a Proof-of-Work receipt
In a worktree from 'start', verify already runs in the background once your edits settle;
'monoflow verify' then prints that result (or waits for it) when the files are unchanged.`,
	"worktree": `  worktree create <agent-id> --packages=<p1,p2> [--deps=<strategy>] [--isolated-deps] [--from-local]
                                      Isolated worktree on branch agent/<agent-id>, cut from origin/main
                                      (freshly fetched); --from-local cuts it from your local main
  worktree list                       List registered worktrees
  worktree focus|update <agent-id> --packages=<...>   Change the projected packages
  worktree sync --to=<agent> --from=<agent>   AST-merge another agent's changes into a worktree
                                      (to update your branch from main, run 'monoflow sync' inside it)
  worktree finish <agent-id> [--force] Remove the worktree and its registry entry
  worktree isolate <agent-id>         Materialize per-worktree dependencies`,
	"pr": `  pr create --title="<t>" [--body="<b>" | --body-file=<path>] [--base=<b>] [--head=<b>] [--draft] [--stack] [--no-push] [--verbose]
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
  events wait [<sha>] [--timeout=<min>]   Block until that commit's CI result arrives (run as a
                                      background task: its exit wakes you; 0 pass/skip, 1 fail)
  events watch <sha> [--branch=<b>] [--timeout=<d>]   Foreground CI watcher (normally started for you)`,
	"outer": `  outer start [--commit=<sha>] [--branch=<b>] [--push] [--revert] [--async]
  outer status [<job-id>] | outer list | outer wait [<job-id>] [--timeout=<d>] [--verbose]   (default: the job for HEAD)`,
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
  sync --stacked-on=<parent-branch>   Stacked PR whose parent was squash-merged: replay only this branch's commits
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
