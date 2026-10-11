package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The push gate asks "is anything uncommitted?". It must not count the branch's own commits
// (getGitChangedFiles falls back to origin/main...HEAD), or every committed branch is blocked.
func TestPushGateIgnoresCommittedBranchChanges(t *testing.T) {
	dir := t.TempDir()
	remote, root := filepath.Join(dir, "remote.git"), filepath.Join(dir, "root")
	git := func(cwd string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(dir, "init", "-q", "--bare", "-b", "main", remote)
	git(dir, "clone", "-q", remote, root)
	git(root, "config", "user.email", "t@t")
	git(root, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0644)
	git(root, "add", "-A")
	git(root, "commit", "-qm", "one")
	git(root, "push", "-q", "origin", "main")
	git(root, "switch", "-qc", "agent/x")
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\n// x\n"), 0644)
	git(root, "commit", "-qam", "change")

	if got := uncommittedFiles(root); len(got) != 0 {
		t.Fatalf("clean tree reported uncommitted files: %v", got)
	}
	if got := getGitChangedFiles(root); len(got) != 1 || got[0] != "a.go" {
		t.Fatalf("branch changes = %v, want [a.go]", got)
	}
	_ = os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\n// y\n"), 0644)
	if got := uncommittedFiles(root); len(got) != 1 {
		t.Fatalf("edit not reported as uncommitted: %v", got)
	}
}
