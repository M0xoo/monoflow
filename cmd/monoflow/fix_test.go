package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// verify formats the agent's changed files with the package's fix_cmd before checking: a badly
// indented Go file is reported as formatted and verify passes, instead of failing on gofmt.
func TestVerifyFormatsChangedFiles(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monoflow")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo := t.TempDir()
	write := func(rel, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0755)
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "MONOFLOW_FORMAT=", "MONOFLOW_NO_SPECULATE=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	write("go.mod", "module example.com/m\n\ngo 1.21\n")
	write("a/a.go", "package a\n\nfunc One() int { return 1 }\n")
	_, _ = run("git", "init", "-q", "-b", "main")
	if out, err := run(bin, "init"); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	_, _ = run("git", "-c", "user.email=t@t", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "add", "-A")
	_, _ = run("git", "-c", "user.email=t@t", "-c", "user.name=t", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "init")

	write("a/a.go", "package a\n\nfunc One() int {\nreturn 1\n}\n")
	out, err := run(bin, "verify")
	if err != nil || !strings.HasPrefix(out, "PASS") || !strings.Contains(out, "formatted: a/a.go") {
		t.Fatalf("verify should format a/a.go and pass, got (%v):\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "a/a.go")); !strings.Contains(string(got), "\treturn 1") {
		t.Fatalf("file not formatted: %q", got)
	}
	if out, _ := run(bin, "verify", "--no-fix"); strings.Contains(out, "formatted:") {
		t.Fatalf("--no-fix must not format: %s", out)
	}
}
