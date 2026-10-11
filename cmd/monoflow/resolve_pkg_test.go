package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

// --package accepts a name, a package path, or a path inside a package (Go: "codes" inside
// the otel root module).
func TestResolvePackageArg(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"codes", "sdk/trace"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0755)
	}
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "go.opentelemetry.io/otel", Path: "."})
	g.AddPackage(&graph.PackageNode{Name: "go.opentelemetry.io/otel/sdk", Path: "sdk"})
	for arg, want := range map[string]string{
		"go.opentelemetry.io/otel/sdk": "go.opentelemetry.io/otel/sdk",
		"codes":                        "go.opentelemetry.io/otel",
		"./sdk/trace":                  "go.opentelemetry.io/otel/sdk",
		"sdk":                          "go.opentelemetry.io/otel/sdk",
	} {
		if p, ok := resolvePackageArg(g, root, arg); !ok || p.Name != want {
			t.Errorf("%s -> %+v, want %s", arg, p, want)
		}
	}
	if _, ok := resolvePackageArg(g, root, "nope"); ok {
		t.Errorf("unknown path must not resolve")
	}
}
