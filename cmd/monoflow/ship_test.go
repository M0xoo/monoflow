package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ship on main with uncommitted work (the agent skipped start) moves that work, new files
// included, onto its own branch and worktree, commits it there, and leaves main clean.
func TestShipFromMainMovesWorkToWorktree(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0755)
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "t@t")
	git(repo, "config", "user.name", "t")
	write("a.txt", "one\n")
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "init")

	write("a.txt", "two\n")
	write("dir/new.txt", "new\n")
	cmd := exec.Command(bin, "ship", "-m", "feat: two and new", "--wait", "--json")
	cmd.Dir = repo
	out, _ := cmd.Output() // pr create fails without a remote; the move and the commit happen first
	var res map[string]any
	_ = json.Unmarshal(out[strings.Index(string(out), "{"):], &res)
	wt, _ := res["moved_to"].(string)
	if wt == "" {
		t.Fatalf("expected moved_to, got %s", out)
	}
	if st := git(repo, "status", "--porcelain"); st != "" {
		t.Fatalf("main must be left clean, got %q", st)
	}
	if b := git(repo, "branch", "--show-current"); b != "main" {
		t.Fatalf("root must stay on main, got %s", b)
	}
	if msg := git(wt, "log", "-1", "--format=%s"); msg != "feat: two and new" {
		t.Fatalf("work must be committed in the worktree, got %q", msg)
	}
	if files := git(wt, "show", "--name-only", "--format="); !strings.Contains(files, "a.txt") || !strings.Contains(files, "dir/new.txt") {
		t.Fatalf("commit must carry the modified and the new file, got %q", files)
	}
}
