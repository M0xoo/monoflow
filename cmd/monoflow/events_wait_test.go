package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/events"
)

// events wait must return as soon as the commit's CI result is in the local inbox, with an
// exit code a harness can act on (0 pass/skip, 1 fail), so a background task wakes the agent.
func TestEventsWaitExitsOnCIResult(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, tc := range []struct {
		sha  string
		typ  events.EventType
		code int
	}{{"aaaaaaaa11", events.EventCIPassed, 0}, {"bbbbbbbb22", events.EventCIFailed, 1}} {
		go func(sha string, typ events.EventType) {
			time.Sleep(500 * time.Millisecond)
			_ = events.NewEventManager(repo).Emit(&events.Event{Type: typ, CommitSHA: sha, Branch: "main", Summary: "ci"})
		}(tc.sha, tc.typ)
		cmd := exec.Command(bin, "events", "wait", tc.sha, "--timeout=1")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(bin)+":"+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if code != tc.code || !strings.Contains(string(out), string(tc.typ)) {
			t.Fatalf("wait %s: exit %d (want %d), output %s", tc.sha, code, tc.code, out)
		}
	}
}
