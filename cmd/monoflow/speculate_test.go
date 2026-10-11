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
	if got != string(stored)+reusedNote {
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

// An agent's own verify stores its result: verify again on unchanged files prints it at once,
// and a second verify started while the first runs waits for it instead of starting over.
func TestVerifyReusesResultForSameFiles(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo := t.TempDir() // not a worktree: no background runs, only the agent's own verify
	write := func(rel, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0755)
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	verify := func() *exec.Cmd {
		cmd := exec.Command(bin, "verify")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MONOFLOW_FORMAT=")
		return cmd
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = repo
		_ = cmd.Run()
	}
	write("go.mod", "module example.com/m\n\ngo 1.21\n")
	write("a/a.go", "package a\n\nfunc One() int { return 1 }\n")
	write("a/a_test.go", "package a\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestOne(t *testing.T) {\n\ttime.Sleep(2 * time.Second)\n\tif One() != 1 {\n\t\tt.Fatal(\"want 1\")\n\t}\n}\n")
	write(".gitignore", "/.agents/verify\n/.agents/cache\n")
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-qm", "init")
	write("a/a.go", "package a\n\nfunc One() int { return 2 }\n")

	// Two verifies at once: the second waits for the first and prints the same result.
	first := verify()
	var firstOut strings.Builder
	first.Stdout = &firstOut
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond)
	second, _ := verify().CombinedOutput()
	_ = first.Wait()
	if !strings.HasPrefix(firstOut.String(), "FAIL") {
		t.Fatalf("first verify should fail, got %s", firstOut.String())
	}
	if !strings.HasPrefix(string(second), firstOut.String()) || !strings.Contains(string(second), "reused:") {
		t.Fatalf("second verify should wait and reuse the first result\nfirst: %s\nsecond: %s", firstOut.String(), second)
	}

	// Again later: instant, same result, failing exit code kept.
	start := time.Now()
	again := verify()
	out, err := again.CombinedOutput()
	if time.Since(start) > 1500*time.Millisecond || !strings.Contains(string(out), "reused:") {
		t.Fatalf("verify on unchanged files should reuse at once, took %v: %s", time.Since(start), out)
	}
	if err == nil {
		t.Fatal("a reused failing result must still exit non-zero")
	}

	// Different files: runs again.
	write("a/a.go", "package a\n\nfunc One() int { return 1 }\n")
	if out, _ := verify().CombinedOutput(); strings.Contains(string(out), "reused:") {
		t.Fatalf("changed files must not reuse: %s", out)
	}
}
