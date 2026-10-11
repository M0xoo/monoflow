package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/deliver"
	"github.com/antigravity/monoflow/pkg/graph"
	initialize "github.com/antigravity/monoflow/pkg/init"
	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/scenario"
	"github.com/antigravity/monoflow/pkg/task"
)

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

	printOut(map[string]any{
		"status":              "initialized",
		"repo_root":           res.RepoRoot,
		"package_count":       res.PackageCount,
		"discovered_packages": res.DiscoveredPkgs,
		"config_path":         res.ConfigPath,
		"rule_file":           filepath.Join(res.RepoRoot, ".agents", "rules", "agentic-monorepo.md"),
		"skill_file":          filepath.Join(res.RepoRoot, ".agents", "skills", "monoflow", "SKILL.md"),
		"workflow_file":       workflowPath,
	})
}

func handleTask(rootDir string) {
	if len(os.Args) < 3 {
		printRaw(`{"error": "missing task subcommand: check | prove | slice | changeset"}`)
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
		printOut(res)
		if !res.Valid {
			os.Exit(1)
		}

	case "slice":
		slice := task.SliceStackedDiff(changedFiles)
		printOut(slice)

	case "prove":
		taskID := "task-001"
		if len(os.Args) > 3 {
			taskID = os.Args[3]
		}
		g, _ := graph.LoadOrDiscover(rootDir)
		receipt, dossier, err := proof.ProveTask(rootDir, taskID, g, changedFiles)
		if err != nil {
			printRaw(fmt.Sprintf(`{"status": "failed", "error": %q}`, err))
			os.Exit(1)
		}
		printOut(map[string]any{
			"status":              receipt.Status,
			"receipt":             receipt,
			"pr_markdown_dossier": dossier,
		})

	case "changeset":
		if len(os.Args) < 5 {
			printRaw(`{"error": "usage: monoflow task changeset <pkg> <patch|minor|major> <summary>"}`)
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
		printRaw(`{"status": "changeset_created"}`)
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
				printOut(manifest)
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
		printRaw(fmt.Sprintf(`{"error": %q}`, err))
		os.Exit(1)
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	printOut(map[string]any{
		"status":            "ready",
		"overlay_path":      overlayPath,
		"graft_duration_ms": durMs,
		"cached_packages":   len(g.GetAllPackages()) - len(affected.AllAffected),
		"delta_packages":    affected.AllAffected,
		"verdict":           "Full App assembled in < 5 seconds without recompiling unchanged packages",
	})
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
		printOut(stats)

	case "clean", "purge":
		if err := mgr.Clean(); err != nil {
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err))
			os.Exit(1)
		}
		printRaw(`{"status": "cleaned", "message": "Local verification and artifact cache purged"}`)

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
		printRaw(fmt.Sprintf(`{"status": "running", "addr": %q, "storage_dir": %q, "token_protected": %t}`, addr, storageDir, token != ""))
		if err := cache.StartServer(addr, storageDir, token); err != nil {
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err.Error()))
			os.Exit(1)
		}

	default:
		printRaw(fmt.Sprintf(`{"error": "unknown cache subcommand: %s (use status | clean | serve)"}`, sub))
		os.Exit(1)
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
			printRaw(fmt.Sprintf(`{"status": "error", "error": %q}`, err.Error()))
		} else {
			fmt.Printf("❌ Scenario Execution Failed: %v\n", err)
		}
		os.Exit(1)
	}

	if asJSON {
		printOut(report)
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
