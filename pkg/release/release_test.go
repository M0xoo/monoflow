package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, s := range files {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		_ = os.WriteFile(p, []byte(s), 0644)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "-A"}, {"commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func edgeOf(g *graph.DependencyGraph, consumer, dep string) graph.Edge {
	for _, e := range g.ScanEdges() {
		if e.Consumer == consumer && e.Dependency == dep {
			return e
		}
	}
	return graph.Edge{}
}

// Pin → the edge becomes pinned at the release (and leaves the affected set); unpin → source.
func TestPinUnpinAcrossEcosystems(t *testing.T) {
	root := tree(t, map[string]string{
		"package.json":                "{\"private\":true,\"workspaces\":[\"packages/*\"]}",
		"packages/ui/package.json":    "{\"name\":\"@acme/ui\",\"version\":\"1.2.0\"}",
		"packages/web/package.json":   "{\n  \"name\": \"@acme/web\",\n  \"dependencies\": { \"@acme/ui\": \"*\", \"left-pad\": \"1.3.0\" }\n}\n",
		"Cargo.toml":                  "[workspace]\nmembers = [\"core\", \"cli\"]\n",
		"core/Cargo.toml":             "[package]\nname = \"core\"\nversion = \"0.4.1\"\n",
		"cli/Cargo.toml":              "[package]\nname = \"cli\"\nversion = \"0.1.0\"\n\n[dependencies]\ncore = { path = \"../core\", version = \"0.4\" }\n",
		"py/base/pyproject.toml":      "[project]\nname = \"acme-base\"\nversion = \"3.1.0\"\n",
		"py/app/pyproject.toml":       "[project]\nname = \"acme-app\"\ndependencies = [\"acme-base>=3\"]\n\n[tool.uv.sources]\nacme-base = { path = \"../base\", editable = true }\n",
	})
	g := graph.New(root)
	if err := g.AutoDiscover(); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ consumer, dep, version string }{
		{"@acme/web", "@acme/ui", "1.2.0"},
		{"cli", "core", "0.4.1"},
		{"acme-app", "acme-base", "3.1.0"},
	}
	for _, c := range cases {
		if e := edgeOf(g, c.consumer, c.dep); e.Mode != graph.EdgeSource {
			t.Fatalf("%s -> %s should start as source, got %+v", c.consumer, c.dep, e)
		}
		if _, err := Create(root, g, c.dep); err != nil {
			t.Fatalf("release %s: %v", c.dep, err)
		}
		if _, err := Materialize(root, g, c.dep, c.version); err != nil {
			t.Fatalf("materialize %s: %v", c.dep, err)
		}
		if _, err := Repoint(root, g, c.consumer, c.dep, c.version); err != nil {
			t.Fatalf("pin %s -> %s: %v", c.consumer, c.dep, err)
		}
	}
	g = graph.New(root)
	_ = g.AutoDiscover()
	for _, c := range cases {
		e := edgeOf(g, c.consumer, c.dep)
		if e.Mode != graph.EdgePinned || e.Constraint != c.version {
			t.Errorf("%s -> %s should be pinned@%s, got %+v", c.consumer, c.dep, c.version, e)
		}
		dep, _ := g.GetPackage(c.dep)
		if all := g.ComputeAffected([]string{filepath.Join(dep.Path, "x")}).AllAffected; len(all) != 1 {
			t.Errorf("changing %s must not affect pinned %s, affected=%v", c.dep, c.consumer, all)
		}
	}
	web := readFile(filepath.Join(root, "packages/web/package.json"))
	if !strings.Contains(web, `"left-pad": "1.3.0"`) || !strings.Contains(web, "file:../../.agents/releases/acme-ui@1.2.0/packages/ui") {
		t.Errorf("package.json rewritten incorrectly:\n%s", web)
	}
	for _, c := range cases {
		if _, err := Repoint(root, g, c.consumer, c.dep, ""); err != nil {
			t.Fatalf("unpin %s: %v", c.consumer, err)
		}
	}
	g = graph.New(root)
	_ = g.AutoDiscover()
	for _, c := range cases {
		if e := edgeOf(g, c.consumer, c.dep); e.Mode != graph.EdgeSource {
			t.Errorf("%s -> %s should be source after unpin, got %+v", c.consumer, c.dep, e)
		}
	}
}

func TestDiffClassifiesBumps(t *testing.T) {
	before := API{"Add": "func(a, b Cents) Cents", "Percent": "func(c Cents, p int64) Cents"}
	if b := RequiredBump(Diff(before, API{"Add": before["Add"], "Percent": before["Percent"], "Sub": "func(a, b Cents) Cents"}), true); b != "minor" {
		t.Errorf("added export should be minor, got %s", b)
	}
	if b := RequiredBump(Diff(before, API{"Add": before["Add"], "Percent": "func(c Cents, p float64) Cents"}), true); b != "major" {
		t.Errorf("changed signature should be major, got %s", b)
	}
	if b := RequiredBump(Diff(before, before), true); b != "patch" {
		t.Errorf("internal change should be patch, got %s", b)
	}
}

func TestTypeScriptSignatureChangesAreBreaking(t *testing.T) {
	before, after := API{}, API{}
	jsSurface(before, "pkg/src/index.ts", "pkg", "export function formatPrice(cents: number): string { return '' }\nexport const toCents = (v: string): number => 0\nexport interface Money { cents: number }\n")
	jsSurface(after, "pkg/src/index.ts", "pkg", "export function formatPrice(cents: bigint): string { return '' }\nexport const toCents = (v: string): bigint => 0n\nexport interface Money { cents: number; currency: string }\n")
	changes := Diff(before, after)
	if RequiredBump(changes, true) != "major" || len(changes) != 3 {
		t.Fatalf("type-only signature changes must be breaking, got %+v", changes)
	}
}
