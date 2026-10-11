package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/graph"
)

// A repo-root check shared by many packages (e.g. root `tsc --noEmit`) runs once per verify.
func TestRepoRootCommandRunsOncePerSuite(t *testing.T) {
	root := t.TempDir()
	_ = exec.Command("git", "-C", root, "init", "-q").Run()
	counter := filepath.Join(t.TempDir(), "runs.log") // outside the repo: verify removes files checks leave in it
	cmd := repoRootPrefix + "echo run >> " + counter
	g := graph.New(root)
	for _, p := range []string{"a", "b", "c"} {
		_ = os.MkdirAll(filepath.Join(root, p), 0755)
		g.AddPackage(&graph.PackageNode{Name: p, Path: p, TypecheckCmd: cmd})
	}
	g.RebuildEdges()
	r := NewRunner(root)
	r.SetCacheManager(cache.NewManager(root, cache.Config{Enabled: false}))
	res := r.VerifyAffected(g, []string{"a/x", "b/x", "c/x"}, VerifyOptions{Strict: true})
	if res.Status != "passed" || res.TotalTargets != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}
	data, _ := os.ReadFile(counter)
	if n := strings.Count(string(data), "run"); n != 1 {
		t.Fatalf("root command should run once for 3 packages, ran %d times", n)
	}
}
