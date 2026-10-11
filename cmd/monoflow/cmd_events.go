package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
)

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
						mode, _ := e.Payload["mode"].(string)
						// Notes for source consumers need no action: --all shows them.
						relevant = mode != graph.EdgeSource && (local == nil && e.RequiresAction || local[consumer])
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
		var shown any = list
		if !hasFlag("--verbose") {
			shown = compactEvents(list)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"scope":                scope,
			"unread_count":         len(list),
			"other_branches_count": others,
			"events":               shown,
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

	case "wait":
		handleEventsWait(rootDir, em)

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

// ciWaitDirective tells an agent how to get woken by its CI result without polling.
func ciWaitDirective(sha string) string {
	return fmt.Sprintf("Do not poll CI. With MonoFlow's harness hooks (Claude Code, opencode) just end your turn: the outer-loop/CI result is delivered to you, and a failure resumes you. Other harnesses: run './bin/monoflow events wait %s' as a background task (exits 0 passed/skipped, 1 failed) or 'monoflow events unread' when resumed.", sha)
}

// handleEventsWait blocks until the CI result for a commit is in the local inbox (written
// by the detached watcher), so a harness background task can wake the agent. It reads
// only local files; GitHub is queried by the watcher, not here.
func handleEventsWait(rootDir string, em *events.EventManager) {
	sha := ""
	timeoutMin := 30
	for _, a := range os.Args[3:] {
		if strings.HasPrefix(a, "--timeout=") {
			if t, err := strconv.Atoi(strings.TrimPrefix(a, "--timeout=")); err == nil {
				timeoutMin = t
			}
		} else if !strings.HasPrefix(a, "-") {
			sha = a
		}
	}
	if sha == "" {
		out, _ := exec.Command("git", "-C", rootDir, "rev-parse", "HEAD").Output()
		sha = strings.TrimSpace(string(out))
	}
	if len(sha) < 7 {
		fmt.Println(`{"error": "usage: monoflow events wait [<commit-sha>] [--timeout=<minutes>]"}`)
		os.Exit(2)
	}
	// Make sure something is watching this commit (idempotent: reuses an active watcher).
	// Without a remote there is no remote CI to watch; just wait on the inbox.
	if exec.Command("git", "-C", rootDir, "remote", "get-url", "origin").Run() == nil {
		cw := events.NewCIWatcher(rootDir)
		_, _ = cw.StartWatch(events.WatchOptions{CommitSHA: sha, Branch: currentBranch(rootDir), Async: true})
	}

	deadline := time.Now().Add(time.Duration(timeoutMin) * time.Minute)
	for time.Now().Before(deadline) {
		list, _ := em.List(false)
		for _, e := range list {
			if !(strings.HasPrefix(string(e.Type), "CI_") || e.Type == events.EventOuterRegression) || e.CommitSHA == "" ||
				!(strings.HasPrefix(e.CommitSHA, sha) || strings.HasPrefix(sha, e.CommitSHA)) {
				continue
			}
			_ = em.Ack(e.ID)
			data, _ := json.MarshalIndent(e, "", "  ")
			fmt.Println(string(data))
			if e.Type == events.EventCIFailed || e.Type == events.EventOuterRegression {
				os.Exit(1)
			}
			os.Exit(0)
		}
		time.Sleep(3 * time.Second)
	}
	fmt.Printf(`{"status": "timeout", "commit": %q, "hint": "no CI result after %d minutes; check 'monoflow events list' and 'monoflow ci health'"}`+"\n", sha, timeoutMin)
	os.Exit(2)
}

// compactEvents keeps what an agent acts on (type, commit, summary, the suggested fix and any
// failure detail); ids, timestamps and run metadata are --verbose.
func compactEvents(list []*events.Event) []map[string]any {
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		m := map[string]any{"type": e.Type, "summary": e.Summary}
		if len(e.CommitSHA) >= 8 {
			m["commit"] = e.CommitSHA[:8]
		}
		if e.RequiresAction {
			m["requires_action"] = true
		}
		if e.SuggestedTask != "" {
			m["suggested_task"] = e.SuggestedTask
		}
		for _, k := range []string{"failure", "failures", "error_summary", "consumer", "dependency", "version", "impact"} {
			if v, ok := e.Payload[k]; ok {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}
