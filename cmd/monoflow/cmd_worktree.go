package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/subset"
	"github.com/antigravity/monoflow/pkg/worktree"
)

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
			"status":         "created",
			"agent_id":       info.AgentID,
			"path":           info.Path,
			"head_sha":       info.HeadSHA,
			"symlinks_count": info.SymlinksCount,
			"isolation":      "own git worktree on branch " + info.Branch + " (packages projected via sparse checkout)",
			"deps_isolated":  info.Isolated,
			"deps_isolation": info.IsolationMethod,
			"is_sparse":      info.IsSparse,
			"duration_ms":    durMs,
			"next":           "cd " + info.Path + " and work only there (same files as the repo root); edit, then monoflow verify",
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
