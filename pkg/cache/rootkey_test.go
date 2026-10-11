package cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

// A root package's key covers its own code only: editing a nested module or the changelog must
// not invalidate it (benchmark: a CHANGELOG.md edit re-verified all 27 opentelemetry-go modules).
func TestRootPackageKeyIgnoresNestedPackagesAndDocs(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("go.mod", "module x\n")
	write("attribute/value.go", "package attribute\n")
	write("CHANGELOG.md", "# v1\n")
	write("sdk/go.mod", "module x/sdk\n")
	write("sdk/sdk.go", "package sdk\n")
	m := NewManager(root, Config{Enabled: true, Dir: t.TempDir()})
	pkg := &graph.PackageNode{Name: "x", Path: ".", Language: "go"}
	key := func() string {
		k, _ := m.ComputePackageKey(pkg, nil, "go test ./...", "test")
		return k
	}
	before := key()
	write("CHANGELOG.md", "# v2\n")
	write("sdk/sdk.go", "package sdk\n\n// changed\n")
	if after := key(); after != before {
		t.Fatal("root key changed on a docs / nested-module edit")
	}
	write("attribute/value.go", "package attribute\n\n// changed\n")
	if key() == before {
		t.Fatal("root key must change when its own code changes")
	}
}

// In a git checkout the key covers what git would commit: an ignored local file (a bin/
// symlink in an agent worktree) must not make the worktree and a clean checkout disagree.
func TestKeyIgnoresGitIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("go.mod", "module x\n")
	write("a.go", "package x\n")
	write(".gitignore", "/bin/\n")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	m := NewManager(root, Config{Enabled: true, Dir: t.TempDir()})
	pkg := &graph.PackageNode{Name: "x", Path: ".", Language: "go"}
	key := func() string {
		k, _ := m.ComputePackageKey(pkg, nil, "go test ./...", "test")
		return k
	}
	before := key()
	write("bin/monoflow", "binary")
	if key() != before {
		t.Fatal("an ignored file changed the key")
	}
	write("b.go", "package x\n")
	if key() == before {
		t.Fatal("a new untracked source file must change the key")
	}
}
