package graph

import (
	"reflect"
	"sort"
	"testing"
)

func TestDependencyGraphBasic(t *testing.T) {
	g := New("/workspace")

	g.AddPackage(&PackageNode{
		Name:     "contracts",
		Path:     "contracts",
		Language: LangProto,
	})
	g.AddPackage(&PackageNode{
		Name:         "shared-types",
		Path:         "packages/shared-types",
		Language:     LangTypeScript,
		Dependencies: []string{"contracts"},
	})
	g.AddPackage(&PackageNode{
		Name:         "services/billing",
		Path:         "services/billing",
		Language:     LangTypeScript,
		Dependencies: []string{"shared-types"},
	})
	g.AddPackage(&PackageNode{
		Name:         "services/auth",
		Path:         "services/auth",
		Language:     LangGo,
		Dependencies: []string{"contracts"},
	})

	g.rebuildEdges()

	// Test Upstream
	upstream := g.GetUpstream("services/billing")
	expectedUpstream := []string{"shared-types"}
	if !reflect.DeepEqual(upstream, expectedUpstream) {
		t.Fatalf("expected upstream %v, got %v", expectedUpstream, upstream)
	}

	// Test Downstream
	downstream := g.GetDownstream("contracts")
	sort.Strings(downstream)
	expectedDownstream := []string{"services/auth", "shared-types"}
	if !reflect.DeepEqual(downstream, expectedDownstream) {
		t.Fatalf("expected downstream %v, got %v", expectedDownstream, downstream)
	}

	// Test Transitive Downstream
	transitive := g.GetTransitiveDownstream("contracts")
	sort.Strings(transitive)
	expectedTransitive := []string{"services/auth", "services/billing", "shared-types"}
	if !reflect.DeepEqual(transitive, expectedTransitive) {
		t.Fatalf("expected transitive downstream %v, got %v", expectedTransitive, transitive)
	}
}

func TestDependencyGraphCircularProtection(t *testing.T) {
	g := New("/workspace")

	// Circular: A -> B -> C -> A
	g.AddPackage(&PackageNode{Name: "pkg-a", Path: "pkg-a", Dependencies: []string{"pkg-b"}})
	g.AddPackage(&PackageNode{Name: "pkg-b", Path: "pkg-b", Dependencies: []string{"pkg-c"}})
	g.AddPackage(&PackageNode{Name: "pkg-c", Path: "pkg-c", Dependencies: []string{"pkg-a"}})

	g.rebuildEdges()

	// Should terminate safely without stack overflow
	trans := g.GetTransitiveDownstream("pkg-a")
	if len(trans) != 2 {
		t.Fatalf("expected 2 transitive dependents in cycle, got %d: %v", len(trans), trans)
	}
}

func TestMapFileToPackage(t *testing.T) {
	g := New("/workspace")

	g.AddPackage(&PackageNode{Name: "services/billing", Path: "services/billing"})
	g.AddPackage(&PackageNode{Name: "services/billing/submodule", Path: "services/billing/submodule"})

	// File in outer billing
	pkg := g.MapFileToPackage("services/billing/src/tax.ts")
	if pkg == nil || pkg.Name != "services/billing" {
		t.Fatalf("expected services/billing, got %v", pkg)
	}

	// File in inner nested package (longest prefix match)
	nestedPkg := g.MapFileToPackage("services/billing/submodule/deep/file.go")
	if nestedPkg == nil || nestedPkg.Name != "services/billing/submodule" {
		t.Fatalf("expected services/billing/submodule, got %v", nestedPkg)
	}

	// Non-matching file
	unmatched := g.MapFileToPackage("random/other/file.txt")
	if unmatched != nil {
		t.Fatalf("expected nil for unmatched file, got %v", unmatched)
	}
}

func TestComputeAffected(t *testing.T) {
	g := New("/workspace")

	g.AddPackage(&PackageNode{Name: "contracts", Path: "contracts"})
	g.AddPackage(&PackageNode{Name: "shared", Path: "packages/shared", Dependencies: []string{"contracts"}})
	g.AddPackage(&PackageNode{Name: "billing", Path: "services/billing", Dependencies: []string{"shared"}})
	g.AddPackage(&PackageNode{Name: "unrelated", Path: "services/unrelated"})

	g.rebuildEdges()

	affected := g.ComputeAffected([]string{"packages/shared/src/index.ts"})

	expectedDirect := []string{"shared"}
	expectedAll := []string{"billing", "shared"}

	if !reflect.DeepEqual(affected.DirectlyChanged, expectedDirect) {
		t.Errorf("expected direct %v, got %v", expectedDirect, affected.DirectlyChanged)
	}
	if !reflect.DeepEqual(affected.AllAffected, expectedAll) {
		t.Errorf("expected all affected %v, got %v", expectedAll, affected.AllAffected)
	}
}

func TestComputeAffectedGlobalFiles(t *testing.T) {
	g := New("/tmp/repo")
	g.AddPackage(&PackageNode{Name: "a", Path: "libs/a"})
	g.AddPackage(&PackageNode{Name: "b", Path: "libs/b"})
	g.RebuildEdges()
	for _, f := range []string{"go.work", "pnpm-lock.yaml", ".github/workflows/ci.yml", "ci/run.sh"} {
		if got := g.ComputeAffected([]string{f}).AllAffected; len(got) != 2 {
			t.Errorf("%s should affect every package, got %v", f, got)
		}
	}
	if got := g.ComputeAffected([]string{"README.md"}).AllAffected; len(got) != 0 {
		t.Errorf("README.md should affect nothing, got %v", got)
	}
}

func TestGetPackageByPathAndShortName(t *testing.T) {
	g := New("/tmp/repo")
	g.AddPackage(&PackageNode{Name: "@acme/ui", Path: "packages/ui"})
	g.AddPackage(&PackageNode{Name: "libs/money", Path: "libs/money"})
	g.AddPackage(&PackageNode{Name: "a/util", Path: "a/util"})
	g.AddPackage(&PackageNode{Name: "b/util", Path: "b/util"})
	for q, want := range map[string]string{"packages/ui": "@acme/ui", "./packages/ui/": "@acme/ui", "ui": "@acme/ui", "money": "libs/money"} {
		if p, ok := g.GetPackage(q); !ok || p.Name != want {
			t.Errorf("GetPackage(%q) = %v, want %s", q, p, want)
		}
	}
	if _, ok := g.GetPackage("util"); ok {
		t.Errorf("ambiguous short name must not resolve")
	}
}
