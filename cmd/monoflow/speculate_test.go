package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An edit in an agent worktree starts verify in the background (from the post-tool-call hook);
// the agent's verify then prints that result for the same tree state instead of re-running,
// and a further edit makes it stale.
func TestSpeculativeVerify(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo := filepath.Join(t.TempDir(), ".agents", "worktrees", "agent1")
	write := func(rel, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0755)
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, args ...string) string {
		cmd := exec.Command(name, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MONOFLOW_FORMAT=")
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	write("go.mod", "module example.com/m\n\ngo 1.21\n")
	write("a/a.go", "package a\n\nfunc One() int { return 1 }\n")
	write("a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {\n\tif One() != 1 {\n\t\tt.Fatal(\"want 1\")\n\t}\n}\n")
	write(".gitignore", "/.agents/verify\n/.agents/cache\n")
	run("git", "init", "-q", "-b", "main")
	run("git", "-c", "user.email=t@t", "-c", "user.name=t", "add", "-A")
	run("git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")

	// The watcher `start` launches (harnesses without hooks) and the hook both trigger it.
	watcher := exec.Command(bin, "speculate-watch")
	watcher.Dir = repo
	if err := watcher.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = watcher.Process.Kill(); _ = watcher.Wait() }()
	time.Sleep(1500 * time.Millisecond)

	write("a/a.go", "package a\n\nfunc One() int { return 2 }\n")
	key := treeStateKey(repo)
	if key == "" {
		t.Fatal("expected a state key for an uncommitted edit")
	}
	outFile := filepath.Join(repo, ".agents", "verify", key+".out")
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := os.Stat(outFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background verify left no result")
		}
		time.Sleep(200 * time.Millisecond)
	}
	stored, _ := os.ReadFile(outFile)
	if !strings.HasPrefix(string(stored), "FAIL") {
		t.Fatalf("background verify should fail, got %s", stored)
	}
	got := run(bin, "verify")
	if got != string(stored) {
		t.Fatalf("verify should print the stored result\nstored: %s\ngot: %s", stored, got)
	}

	// A pid file left by a killed run must not make verify wait.
	_ = os.Remove(outFile)
	_ = os.WriteFile(filepath.Join(repo, ".agents", "verify", key+".pid"), []byte("999999"), 0644)
	start := time.Now()
	if got := run(bin, "verify", "--affected"); !strings.HasPrefix(got, "FAIL") || time.Since(start) > 60*time.Second {
		t.Fatalf("verify with a stale pid file should run normally, got %s after %v", got, time.Since(start))
	}

	// A different state: the stored result does not apply.
	write("a/a.go", "package a\n\nfunc One() int { return 1 }\n")
	if got := run(bin, "verify"); !strings.HasPrefix(got, "PASS") {
		t.Fatalf("verify after the fix should run and pass, got %s", got)
	}
}
