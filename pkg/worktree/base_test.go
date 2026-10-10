package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A shared root whose main was never pulled must not hand new agents stale code: the worktree
// starts from origin/main when local main is behind it (found when a worktree was cut from a
// main 12 PRs old).
func TestCreateStartsFromRemoteWhenLocalBaseIsStale(t *testing.T) {
	dir := t.TempDir()
	remote, root, other := filepath.Join(dir, "remote.git"), filepath.Join(dir, "root"), filepath.Join(dir, "other")
	git := func(cwd string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(dir, "init", "-q", "--bare", "-b", "main", remote)
	git(dir, "clone", "-q", remote, root)
	git(root, "config", "user.email", "t@t")
	git(root, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("1\n"), 0644)
	git(root, "add", "-A")
	git(root, "commit", "-qm", "one")
	git(root, "push", "-q", "origin", "main")

	// Someone else merges to main; the root never pulls.
	git(dir, "clone", "-q", remote, other)
	git(other, "config", "user.email", "t@t")
	git(other, "config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(other, "a.txt"), []byte("2\n"), 0644)
	git(other, "commit", "-qam", "two")
	git(other, "push", "-q", "origin", "main")
	want := git(other, "rev-parse", "HEAD")

	info, err := NewManager(root).CreateWithOpts("fresh", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := git(info.Path, "rev-parse", "HEAD"); got != want {
		t.Fatalf("worktree started from %s, want origin/main %s", got, want)
	}
	if got := git(root, "rev-parse", "main"); got == want {
		t.Fatal("local main must be left alone")
	}
}
