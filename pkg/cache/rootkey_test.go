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

// A loose gitignored file a test may read (live check: a test fixture listed in .gitignore) must
// change the key, or the outer loop's clean checkout reuses a green result that depended on it.
// OS junk and ignored directories (build outputs) must not.
func TestKeyCoversIgnoredLooseFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("go.mod", "module x\n")
	write("a.go", "package x\n")
	write(".gitignore", "/bin/\nlocal.txt\n.DS_Store\n")
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	m := NewManager(root, Config{Enabled: true, Dir: t.TempDir()})
	pkg := &graph.PackageNode{Name: "x", Path: ".", Language: "go"}
	key := func() string {
		k, _ := m.ComputePackageKey(pkg, nil, "go test ./...", "test")
		return k
	}
	clean := key()
	write(".DS_Store", "junk")
	write("bin/tool", "binary")
	if key() != clean {
		t.Fatal("OS junk or an ignored build directory changed the key")
	}
	write("local.txt", "hi")
	if key() == clean {
		t.Fatal("an ignored loose file must change the key")
	}
}

// As an upstream ("source" key), a package's own tests and snapshots do not count: a dependent
// cannot import them. Its own check key still covers them, and its source code counts for both.
func TestSourceKeyIgnoresTestFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("server/src/mw.ts", "export const code = 400\n")
	write("server/src/mw.test.ts", "expect(code).toBe(400)\n")
	write("server/src/__snapshots__/mw.test.ts.snap", "400\n")
	m := NewManager(root, Config{Enabled: true, Dir: t.TempDir()})
	pkg := &graph.PackageNode{Name: "server", Path: "server", Language: "typescript"}
	source := func() string { k, _ := m.ComputePackageKey(pkg, nil, "", "source"); return k }
	own := func() string { k, _ := m.ComputePackageKey(pkg, nil, "vitest run", "test"); return k }
	s0, o0 := source(), own()
	write("server/src/mw.test.ts", "expect(code).toBe(422)\n")
	write("server/src/__snapshots__/mw.test.ts.snap", "422\n")
	if source() != s0 {
		t.Fatal("an upstream's test edit must not change its source key")
	}
	if own() == o0 {
		t.Fatal("the package's own check key must cover its tests")
	}
	write("server/src/mw.ts", "export const code = 422\n")
	if source() == s0 {
		t.Fatal("a source edit must change the source key")
	}
}

// A Python member's environment is pinned by the workspace lockfile above it: a dependency
// bump there must not reuse its cached pass.
func TestPythonKeyCoversWorkspaceLockfile(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("pyproject.toml", "[tool.uv.workspace]\nmembers = [\"packages/*\"]\n")
	write("uv.lock", "version = 1\n")
	write("packages/a/pyproject.toml", "[project]\nname = \"a\"\n")
	write("packages/a/src/a/__init__.py", "X = 1\n")
	m := NewManager(root, Config{Enabled: true, Dir: t.TempDir()})
	pkg := &graph.PackageNode{Name: "a", Path: "packages/a", Language: graph.LangPython}
	key := func() string {
		k, _ := m.ComputePackageKey(pkg, nil, "uv run pytest -q", "test")
		return k
	}
	before := key()
	write("uv.lock", "version = 1\n# requests 2.32 -> 2.33\n")
	if key() == before {
		t.Fatal("key must change when the workspace uv.lock changes")
	}
}
