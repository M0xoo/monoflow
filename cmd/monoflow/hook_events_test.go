package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/events"
)

// The Stop hook must wake the agent (block with the event) when something needs action, and
// let it stop once that event was delivered (no infinite loop).
func TestHookStopBlocksOnActionableEvent(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "agent/x"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	_ = events.NewEventManager(repo).Emit(&events.Event{Type: events.EventOuterRegression, CommitSHA: "abc", Branch: "agent/x",
		RequiresAction: true, Summary: "outer loop red", Payload: map[string]any{"error_summary": "billing_test.go:12: want 935"}})

	run := func(sub ...string) string {
		cmd := exec.Command(bin, append([]string{"hook"}, sub...)...)
		cmd.Dir = repo
		cmd.Stdin = strings.NewReader(`{"stop_hook_active": false}`)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	out := run("stop", "--wait=0")
	var resp struct{ Decision, Reason string }
	if json.Unmarshal([]byte(out), &resp) != nil || resp.Decision != "block" || !strings.Contains(resp.Reason, "want 935") {
		t.Fatalf("stop hook should block with the regression, got %q", out)
	}
	if out := run("stop", "--wait=0"); strings.TrimSpace(out) != "" {
		t.Fatalf("second stop must be allowed (event already delivered), got %q", out)
	}
	if out := run("deliver", "--event=PostToolUse"); strings.TrimSpace(out) != "" {
		t.Fatalf("delivered events must not be injected again, got %q", out)
	}
}
