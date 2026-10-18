package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// After a session that used MonoFlow, the Stop hook asks once for a retro; the agent sees
// what it ran (including commands MonoFlow redirected), files a suggestion and closes it.
func TestImproveRetroAfterSession(t *testing.T) {
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
	run := func(stdin string, args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MONOFLOW_INTERNAL=", "MONOFLOW_FORMAT=", "MONOFLOW_IMPROVE_DIR=")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	if out, _ := run("", "hook", "stop", "--wait=0"); strings.TrimSpace(out) != "" {
		t.Fatalf("no MonoFlow work yet: no retro request, got %q", out)
	}
	run("", "find", "nothing-here")
	run("", "graph", "query", "no-such-package")
	if _, code := run("git commit -m x", "hook", "check"); code != 2 {
		t.Fatalf("raw git commit must be redirected, exit %d", code)
	}
	out, _ := run(`{"stop_hook_active": false}`, "hook", "stop", "--wait=0")
	var resp struct{ Decision, Reason string }
	if json.Unmarshal([]byte(out), &resp) != nil || resp.Decision != "block" {
		t.Fatalf("stop hook should ask for a retro, got %q", out)
	}
	for _, want := range []string{"find 1", "graph 1", "git commit -m x", "improve add"} {
		if !strings.Contains(resp.Reason, want) {
			t.Errorf("retro request lacks %q:\n%s", want, resp.Reason)
		}
	}
	if out, _ := run(`{"stop_hook_active": true}`, "hook", "stop", "--wait=0"); strings.TrimSpace(out) != "" {
		t.Fatalf("the retro is asked for once, got %q", out)
	}
	if out, code := run("", "improve", "add", "graph query should suggest close package names", "--area=graph", "--kind=friction", "--evidence=graph query no-such-package"); code != 0 || !strings.Contains(out, "filed s") {
		t.Fatalf("improve add: %d %s", code, out)
	}
	if out, code := run("", "improve", "add", "x", "--bogus=1"); code != 1 || !strings.Contains(out, "unknown flag") {
		t.Fatalf("unknown flags are refused: %d %s", code, out)
	}
	if out, _ := run("", "improve", "done"); !strings.Contains(out, "1 suggestion") {
		t.Fatalf("improve done: %s", out)
	}
	out, _ = run("", "improve", "triage")
	if !strings.Contains(out, "graph query should suggest close package names") || !strings.Contains(out, "1 retros") {
		t.Fatalf("triage: %s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".agents", "improve", "TRIAGE.md")); err != nil {
		t.Fatal("triage writes TRIAGE.md next to the suggestions")
	}
	if out, _ := run("", "improve"); !strings.Contains(out, "0 monoflow commands") {
		t.Fatalf("a closed retro starts a fresh session: %s", out)
	}
}

// verify and ship run in the clean main checkout while the agent's edits sit in its worktree:
// say where the work is instead of "passing" nothing and shipping an empty branch.
func TestStrayWorkInMainCheckout(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	dir := t.TempDir()
	remote, repo := filepath.Join(dir, "remote.git"), filepath.Join(dir, "repo")
	wt := filepath.Join(repo, ".agents", "worktrees", "a1")
	git := func(d string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "MONOFLOW_INTERNAL=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(dir, "init", "-q", "--bare", "-b", "main", remote)
	git(dir, "init", "-q", "-b", "main", repo)
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0644)
	_ = os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("/.agents/worktrees\n"), 0644)
	for _, args := range [][]string{{"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "-A"}, {"commit", "-qm", "init"},
		{"remote", "add", "origin", remote}, {"push", "-q", "origin", "main"}, {"fetch", "-q"}, {"worktree", "add", "-q", "-b", "agent/a1", wt}} {
		git(repo, args...)
	}
	_ = os.WriteFile(filepath.Join(wt, "a.txt"), []byte("changed\n"), 0644)
	run := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MONOFLOW_INTERNAL=", "MONOFLOW_FORMAT=", "MONOFLOW_NO_SPECULATE=1", "MONOFLOW_NO_WARM=1")
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return string(out), code
	}
	for _, c := range []string{"verify", "ship"} {
		args := []string{c}
		if c == "ship" {
			args = append(args, "-m", "x")
		}
		out, code := run(args...)
		if code == 0 || !strings.Contains(out, "nothing to "+c) || !strings.Contains(out, wt) {
			t.Fatalf("%s in the clean main checkout must point at the worktree (exit %d):\n%s", c, code, out)
		}
	}
	if out, _ := run("branch"); strings.Contains(out, "feat/pr-") {
		t.Fatalf("ship must not create a branch: %s", out)
	}
}
