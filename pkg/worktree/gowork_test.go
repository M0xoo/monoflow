package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/guard"
)

// A sparse worktree prunes go.work so Go builds work; that projection must never be
// committed (round-2 simulation: it broke remote CI for every Go module).
func TestSparseWorktreeNeverCommitsProjectedGoWork(t *testing.T) {
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
		return string(out)
	}
	write := func(rel, content string) {
		p := filepath.Join(repo, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "t@t")
	git(repo, "config", "user.name", "t")
	write("go.work", "go 1.22\n\nuse (\n\t./libs/a\n\t./libs/b\n)\n")
	write("libs/a/go.mod", "module x/a\n\ngo 1.22\n")
	write("libs/a/a.go", "package a\n")
	write("libs/b/go.mod", "module x/b\n\ngo 1.22\n")
	write("libs/b/b.go", "package b\n")
	write("web/package.json", `{"name":"web","version":"1.0.0"}`)
	git(repo, "add", "-A")
	git(repo, "commit", "-qm", "init")

	m := NewManager(repo)
	info, err := m.CreateWithOpts("agent-x", CreateOptions{Packages: []string{"libs/a"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gw, _ := os.ReadFile(filepath.Join(info.Path, "go.work"))
	if strings.Contains(string(gw), "./libs/b") {
		t.Fatalf("go.work should be pruned to projected modules, got:\n%s", gw)
	}

	_ = os.WriteFile(filepath.Join(info.Path, "libs/a/a.go"), []byte("package a\n\nfunc A() {}\n"), 0644)
	if _, err := guard.FastAgentCommit(info.Path, "feat: a"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	files := git(info.Path, "show", "--name-only", "--format=", "HEAD")
	if strings.Contains(files, "go.work") || strings.Contains(files, ".agents") {
		t.Fatalf("commit must contain only the agent's change, got:\n%s", files)
	}
	if !strings.Contains(files, "libs/a/a.go") {
		t.Fatalf("expected libs/a/a.go in commit, got:\n%s", files)
	}
}
