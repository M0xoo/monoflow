package verify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/graph"
)

// A dependent's cached "passed" must not survive a change to its dependency (round-4
// e2e: billing stayed cached-green after libs/money removed a function it calls).
func TestDependencyChangeInvalidatesDependentCache(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	write("lib/api.txt", "Add\n")
	write("app/uses.txt", "Add\n")
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "lib", Path: "lib"})
	// app passes only while lib still provides every symbol app uses.
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Dependencies: []string{"lib"},
		TestCmd: `for s in $(cat uses.txt); do grep -qx "$s" ../lib/api.txt || { echo "undefined: $s"; exit 1; }; done`})
	g.RebuildEdges()
	r := NewRunner(root)
	r.SetCacheManager(cache.NewManager(root, cache.Config{Enabled: true, Dir: filepath.Join(root, ".cache")}))
	r.SetGraph(g)
	app, _ := g.GetPackage("app")

	if res := r.VerifyTarget(app, VerifyOptions{Strict: true}); res.Status != "passed" {
		t.Fatalf("baseline should pass: %+v", res)
	}
	if res := r.VerifyTarget(app, VerifyOptions{Strict: true}); !res.Cached {
		t.Fatalf("unchanged inputs should be a cache hit")
	}
	write("lib/api.txt", "Sum\n") // breaking change in the dependency
	res := r.VerifyTarget(app, VerifyOptions{Strict: true})
	if res.Cached || res.Status != "failed" {
		t.Fatalf("dependency change must invalidate the dependent's cache and fail it, got cached=%v status=%s", res.Cached, res.Status)
	}
}
