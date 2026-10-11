package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A branch conflicting with origin/main is not pushed or opened as a PR: the agent gets the
// files and a PR_CONFLICT event. A pushed branch whose base moved into a conflict later is
// reported when the agent reads its events.
func TestConflictsWithBaseAreReported(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	remote, repo, other := t.TempDir(), t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	mf := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MONOFLOW_FORMAT=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	write := func(dir, rel, s string) { _ = os.WriteFile(filepath.Join(dir, rel), []byte(s), 0644) }

	git(remote, "init", "-q", "--bare", "-b", "main")
	git(repo, "init", "-q", "-b", "main")
	write(repo, "a.txt", "one\n")
	write(repo, "b.txt", "one\n")
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "init")
	git(repo, "remote", "add", "origin", remote)
	git(repo, "push", "-q", "origin", "main")
	git(other, "clone", "-q", remote, ".")

	// 1. The branch and origin/main change the same line: nothing is pushed.
	git(repo, "switch", "-q", "-c", "agent/x")
	write(repo, "a.txt", "agent\n")
	git(repo, "commit", "-qam", "agent change")
	write(other, "a.txt", "someone else\n")
	git(other, "commit", "-qam", "main moved")
	git(other, "push", "-q", "origin", "main")
	out, err := mf("pr", "create", "--title=agent change")
	if err == nil || !strings.Contains(out, "conflicts with origin/main") || !strings.Contains(out, "a.txt") {
		t.Fatalf("pr create must refuse a conflicting branch, got %v:\n%s", err, out)
	}
	if strings.Contains(git(remote, "branch", "--list"), "agent/x") {
		t.Fatal("a conflicting branch must not be pushed")
	}
	if out, _ := mf("events", "unread"); !strings.Contains(out, "PR_CONFLICT") {
		t.Fatalf("PR_CONFLICT event expected:\n%s", out)
	}

	// 2. A branch pushed clean; then main moves into a conflict: reported on events unread.
	git(repo, "switch", "-q", "main")
	git(repo, "pull", "-q", "origin", "main")
	git(repo, "switch", "-q", "-c", "agent/y")
	write(repo, "b.txt", "agent y\n")
	git(repo, "commit", "-qam", "agent y")
	git(repo, "push", "-q", "origin", "agent/y")
	git(other, "pull", "-q", "origin", "main")
	write(other, "b.txt", "main again\n")
	git(other, "commit", "-qam", "main moved again")
	git(other, "push", "-q", "origin", "main")
	if out, _ := mf("events", "unread"); !strings.Contains(out, "b.txt") {
		t.Fatalf("a pushed branch that now conflicts must be reported:\n%s", out)
	}
}
