package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/antigravity/monoflow/pkg/agenthooks"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/verify"
)

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
	case "deliver":
		hookDeliver(rootDirForHook())
	case "stop":
		hookStop(rootDirForHook())
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

func rootDirForHook() string {
	if root, err := findRepoRoot(); err == nil {
		return root
	}
	return "."
}

func hookFlag(name string) string {
	for _, a := range os.Args[3:] {
		if strings.HasPrefix(a, "--"+name+"=") {
			return strings.TrimPrefix(a, "--"+name+"=")
		}
	}
	return ""
}

// hookDeliver injects events the agent has not seen yet into its context. Claude Code:
// PostToolUse / UserPromptSubmit / SessionStart hooks (--event=<name>, JSON output);
// other harnesses: --format=text prints plain text (nothing when there is nothing new).
func hookDeliver(root string) {
	_ = readHookStdin() // hook payload is not needed
	maybeVerifyInBackground(root)
	branch := currentBranch(root)
	em := events.NewEventManager(root)
	pending := em.Pending(branch, branch, projectedPackages(root))
	if hookFlag("only") == "action" {
		var act []*events.Event
		for _, e := range pending {
			if e.RequiresAction {
				act = append(act, e)
			}
		}
		pending = act
	}
	if len(pending) == 0 {
		return
	}
	_ = em.MarkDelivered(branch, pending)
	text := events.Render(pending)
	if hookFlag("format") == "text" {
		fmt.Print(text)
		return
	}
	event := firstNonEmpty(hookFlag("event"), "PostToolUse")
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": event, "additionalContext": text}})
	fmt.Println(string(out))
}

// hookStop runs when the agent is about to end its turn (Claude Code Stop hook). If an
// outer loop or CI run for this branch is in flight it waits for the result (bounded by
// agent_events.stop_wait_minutes), then blocks the stop with any event that needs action,
// so the agent resumes and handles it without running a command.
func hookStop(root string) {
	var payload struct {
		StopHookActive bool `json:"stop_hook_active"`
	}
	_ = json.Unmarshal(readHookStdin(), &payload)
	branch := currentBranch(root)
	wait := stopWaitMinutes(root)
	if hookFlag("wait") != "" {
		if n, err := strconv.Atoi(hookFlag("wait")); err == nil {
			wait = n
		}
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Minute)
	for wait > 0 && inFlight(root, branch) && time.Now().Before(deadline) {
		time.Sleep(3 * time.Second)
	}
	em := events.NewEventManager(root)
	var act []*events.Event
	for _, e := range em.Pending(branch, branch, projectedPackages(root)) {
		if e.RequiresAction {
			act = append(act, e)
		}
	}
	if len(act) == 0 {
		return // nothing needs the agent: let it stop
	}
	_ = em.MarkDelivered(branch, act)
	reason := events.Render(act) + "Handle these before finishing (they block your change from landing)."
	if hookFlag("format") == "text" {
		fmt.Print(reason)
		return
	}
	out, _ := json.Marshal(map[string]any{"decision": "block", "reason": reason})
	fmt.Println(string(out))
}

// inFlight reports outer-loop jobs, background ships or CI watches still running for branch.
func inFlight(root, branch string) bool {
	// A background `monoflow ship` (push + PR once the outer loop is green) can still fail
	// after the outer loop finished: wait for it too, so its SHIP_FAILED event is delivered.
	if markers, _ := filepath.Glob(filepath.Join(root, ".agents", "ship", "*.inflight")); len(markers) > 0 {
		for _, m := range markers {
			fi, err := os.Stat(m)
			if err != nil || time.Since(fi.ModTime()) > 30*time.Minute || !shipProcessAlive(m) {
				_ = os.Remove(m) // stale: its process is gone (killed with its agent session)
				continue
			}
			return true
		}
	}
	if jobs, err := verify.NewOuterLoopManager(root).ListJobs(); err == nil {
		for _, j := range jobs {
			if j.Branch == branch && (j.Status == verify.JobQueued || j.Status == verify.JobRunning) {
				return true
			}
		}
	}
	if jobs, err := events.NewCIWatcher(root).ListJobs(); err == nil {
		for _, j := range jobs {
			if j.Branch == branch && j.Status == events.WatchStatusMonitoring && time.Since(j.StartedAt) < 2*time.Hour {
				return true
			}
		}
	}
	return false
}

func stopWaitMinutes(root string) int {
	data, err := os.ReadFile(filepath.Join(root, ".agents", "monoflow.config.json"))
	if err != nil {
		return 15
	}
	var cfg struct {
		AgentEvents *struct {
			StopWaitMinutes *int `json:"stop_wait_minutes"`
		} `json:"agent_events"`
	}
	if json.Unmarshal(data, &cfg) == nil && cfg.AgentEvents != nil && cfg.AgentEvents.StopWaitMinutes != nil {
		return *cfg.AgentEvents.StopWaitMinutes
	}
	return 15
}

// readHookStdin reads a hook's JSON payload without ever blocking: some harnesses leave
// stdin open (or attached to nothing), so give up after a short delay.
func readHookStdin() []byte {
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return nil // interactive terminal: no payload
	}
	ch := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(os.Stdin)
		ch <- data
	}()
	select {
	case data := <-ch:
		return data
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

// shipProcessAlive reports whether the ship process named by a "<pid>.inflight" marker runs.
func shipProcessAlive(marker string) bool {
	pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(marker), ".inflight"))
	if err != nil {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
