package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/antigravity/monoflow/pkg/pr"
	"github.com/antigravity/monoflow/pkg/subset"
)

func handlePR(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`MonoFlow Pull Request Commands:
  monoflow pr create [--title="..."] [--body="..." | --body-file=<path>] [--base=main] [--draft] [--stack] [--no-push] [--verbose]
  monoflow pr list [--state=open|closed|merged|all] [--limit=N]
  monoflow pr view <id>
  monoflow pr status [id] [--json]
  monoflow pr review <id> [--approve] [--comment="..."] [--reject]
  monoflow pr test <id> [--against-master] [--base=main]
  monoflow pr local <id> [--worktree=<id>] [--deps=contracts|source|none]
  monoflow pr merge <id> [--strategy=squash|merge|rebase] [--queue] [--delete-branch]`)
		exit(1)
	}

	mgr := pr.NewManager(rootDir)
	sub := os.Args[2]

	switch sub {
	case "create":
		opts := pr.CreateOptions{
			BaseBranch: "main",
			AutoPush:   true,
		}
		verbose := false
		for _, arg := range os.Args[3:] {
			if strings.HasPrefix(arg, "--title=") {
				opts.Title = strings.TrimPrefix(arg, "--title=")
			} else if strings.HasPrefix(arg, "--body=") {
				opts.Body = strings.TrimPrefix(arg, "--body=")
			} else if strings.HasPrefix(arg, "--body-file=") {
				b, err := os.ReadFile(strings.TrimPrefix(arg, "--body-file="))
				if err != nil {
					printOut(map[string]any{"error": "cannot read --body-file: " + err.Error()})
					exit(2)
				}
				opts.Body = string(b)
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
			} else if arg == "--verbose" {
				verbose = true
			} else if strings.HasPrefix(arg, "-") {
				// A silently ignored flag (e.g. --body-file before it existed) loses the agent's input.
				printOut(map[string]any{"error": "unknown flag for pr create: " + arg + " (see 'monoflow pr create --help')"})
				exit(2)
			}
		}

		// A PR is outward-facing: never open one under an invented title (an agent's
		// "pr create --help" on an old binary opened PR #23 as "feat(...): agent changes on ...").
		// The branch's own commit message is the agent's words, not an invented title.
		if strings.TrimSpace(opts.Title) == "" {
			opts.Title = ownCommitSubject(rootDir, opts.BaseBranch)
		}
		if strings.TrimSpace(opts.Title) == "" {
			printOut(map[string]any{"error": `pr create needs --title="<PR title>" (no commit on this branch to take it from); nothing was created. Usually 'monoflow ship -m "<msg>"' does commit, push and PR in one call`})
			exit(2)
		}

		if base, files, checked := baseConflicts(rootDir); checked && len(files) > 0 {
			emitConflict(rootDir, base, files)
			printOut(map[string]any{"status": "blocked", "error": fmt.Sprintf("your branch conflicts with origin/%s; nothing was pushed or opened", base), "conflicts": files, "next": conflictNext(base, files)})
			exit(1)
		}

		res, err := mgr.Create(opts)
		if err != nil {
			var gateErr *pr.GateError
			if errors.As(err, &gateErr) {
				printOut(map[string]any{
					"status":        "blocked",
					"error":         err.Error(),
					"proof_status":  gateErr.ProofStatus,
					"failed_checks": gateErr.FailedChecks,
					"audit":         gateErr.Audit,
				})
				exit(1)
			}
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err.Error()))
			exit(1)
		}
		var out any = res
		if !verbose {
			out = compactPR(res)
		}
		printOut(out)

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
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			exit(1)
		}
		printOut(list)

	case "view":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing pr-id for monoflow pr view"}`)
			exit(1)
		}
		prID := os.Args[3]
		meta, err := mgr.View(prID)
		if err != nil {
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			exit(1)
		}
		printOut(meta)

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
				printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err.Error()))
			} else {
				fmt.Printf("❌ Failed to get PR status: %v\n", err)
			}
			exit(1)
		}

		switch {
		case asJSON:
			printOut(res)
		case hasFlag("--verbose") || stdoutIsTerminal():
			fmt.Println(res.FormatView())
		default:
			// Agents read this through a pipe: the 12 KB dossier view cost them context for a
			// handful of facts. --verbose (or a terminal) shows the full view.
			printOut(compactPRStatus(res))
		}

	case "review":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing pr-id for monoflow pr review"}`)
			exit(1)
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
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			exit(1)
		}
		printOut(res)

	case "test":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing pr-id for monoflow pr test"}`)
			exit(1)
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
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			exit(1)
		}
		printOut(res)

	case "local":
		if len(os.Args) < 4 {
			printRaw(`{"error": "usage: monoflow pr local <pr-id> [--worktree=<id>] [--deps=contracts|source|none]"}`)
			exit(1)
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
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			exit(1)
		}
		printOut(res)

	case "merge":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing pr-id for monoflow pr merge"}`)
			exit(1)
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
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			exit(1)
		}
		printOut(res)

	default:
		// Fallback for previous monoflow pr test <id> syntax where id was directly second arg
		opts := pr.TestOptions{BaseBranch: "main"}
		res, err := mgr.Test(sub, opts)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": "unknown pr subcommand '%s'; usage: monoflow pr [create|list|view|status|review|test|local|merge]"}`, sub))
			exit(1)
		}
		printOut(res)
	}
}

// compactPR is what an agent needs after 'pr create': where the PR is, whether it was proven
// and pushed, and how to wait for CI. The full dossier (~6 KB) is in the PR body and behind
// --verbose; printing it cost every agent context for nothing.
func compactPR(res *pr.PRMetadata) map[string]any {
	out := map[string]any{
		"id":                res.ID,
		"state":             res.State,
		"head_sha":          res.HeadSHA,
		"affected_packages": res.AffectedPackages,
		"pushed":            res.Pushed,
	}
	if res.GitHubURL != "" {
		out["url"] = res.GitHubURL
	}
	if res.Proof != nil {
		out["proof"] = res.Proof.Status
		var failed []string
		for _, c := range res.Proof.DoDChecks {
			if c.Status != "PASSED" {
				failed = append(failed, c.ID+": "+c.Evidence)
			}
		}
		if len(failed) > 0 {
			out["checks_not_passed"] = failed
		}
	}
	if res.Risk != nil {
		out["risk"] = res.Risk.Tier
	}
	if res.CIWaitCommand != "" {
		out["ci_wait_command"] = res.CIWaitCommand
	}
	if res.RemotePR != nil {
		out["remote_pr"] = res.RemotePR
	}
	return out
}

func compactPRStatus(res *pr.PRStatusResult) map[string]any {
	out := map[string]any{
		"pr_id":           res.PRID,
		"state":           res.RemoteState,
		"proof":           res.ProofStatus,
		"dod":             fmt.Sprintf("%d/%d passed", res.DoDPassedChecks, res.DoDTotalChecks),
		"unified_verdict": res.UnifiedVerdict,
	}
	if res.GitHubURL != "" {
		out["url"] = res.GitHubURL
	}
	if res.RemoteCIStatus != "" {
		out["remote_ci"] = res.RemoteCIStatus
	}
	if res.ReviewDecision != "" || res.RemoteReviewDecision != "" {
		out["review"] = strings.TrimSpace(res.ReviewDecision + " " + res.RemoteReviewDecision)
	}
	if res.Mergeable != "" {
		out["mergeable"] = res.Mergeable
	}
	if res.SyncState != "" && res.SyncState != "IN_SYNC" {
		out["sync"] = res.SyncState
	}
	if res.ProofReceipt != nil {
		var failed []string
		for _, c := range res.ProofReceipt.DoDChecks {
			if c.Status != "PASSED" {
				failed = append(failed, c.ID+": "+c.Evidence)
			}
		}
		if len(failed) > 0 {
			out["checks_not_passed"] = failed
		}
	}
	return out
}

func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// ownCommitSubject is the subject of the branch's latest commit when the branch has commits
// of its own (ahead of origin/<base>), else "".
func ownCommitSubject(rootDir, base string) string {
	b := currentBranch(rootDir)
	if b == "" || b == base || b == "HEAD" {
		return ""
	}
	out, err := exec.Command("git", "-C", rootDir, "log", "-1", "--format=%s", "origin/"+base+"..HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
