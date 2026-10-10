package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/verify"
)

// The outer loop runs the repo's CI command on a full checkout even from a sparse worktree
// (benchmark: every sparse agent's commit was falsely red because ci/run.sh was missing).
func TestOuterLoopFromSparseWorktreeRunsRepoCI(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("libs/a/go.mod", "module x/a\n\ngo 1.22\n")
	write("libs/a/a.go", "package a\n")
	write("libs/b/go.mod", "module x/b\n\ngo 1.22\n")
	write("libs/b/b.go", "package b\n")
	write("ci/run.sh", "test -f libs/a/a.go && test -f libs/b/b.go\n")
	write(".agents/monoflow.config.json", `{"version":"1.0","ci_cmd":"bash ci/run.sh","packages":[{"name":"libs/a","path":"libs/a","language":"generic","dependencies":[]},{"name":"libs/b","path":"libs/b","language":"generic","dependencies":[]}]}`)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	info, err := NewManager(root).CreateWithOpts("sparse-agent", CreateOptions{Packages: []string{"libs/a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "libs/b")); err == nil {
		t.Skip("worktree is not sparse in this environment")
	}
	_ = os.WriteFile(filepath.Join(info.Path, "libs/a/a.go"), []byte("package a\n\n// change\n"), 0644)
	for _, args := range [][]string{{"add", "-A", "libs"}, {"commit", "-qm", "change a"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = info.Path
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	job, err := verify.NewOuterLoopManager(info.Path).StartJob(verify.OuterLoopOptions{IncludeE2E: true})
	if err != nil || job.Status != verify.JobPassed {
		t.Fatalf("outer loop from a sparse worktree must pass (ci_cmd needs the full repo): status=%v err=%v summary=%s", job.Status, err, job.ErrorSummary)
	}
}
