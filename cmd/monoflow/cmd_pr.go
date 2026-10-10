package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/antigravity/monoflow/pkg/pr"
	"github.com/antigravity/monoflow/pkg/subset"
)

func handlePR(rootDir string) {
	if len(os.Args) < 3 {
		fmt.Println(`MonoFlow Pull Request Commands:
  monoflow pr create [--title="..."] [--body="..." | --body-file=<path>] [--base=main] [--draft] [--stack] [--no-push]
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
			} else if strings.HasPrefix(arg, "--body-file=") {
				b, err := os.ReadFile(strings.TrimPrefix(arg, "--body-file="))
				if err != nil {
					fmt.Printf("{\"error\": %q}\n", "cannot read --body-file: "+err.Error())
					os.Exit(2)
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
			} else if strings.HasPrefix(arg, "-") {
				// A silently ignored flag (e.g. --body-file before it existed) loses the agent's input.
				fmt.Printf("{\"error\": %q}\n", "unknown flag for pr create: "+arg+" (see 'monoflow pr create --help')")
				os.Exit(2)
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
