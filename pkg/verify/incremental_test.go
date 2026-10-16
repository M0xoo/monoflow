package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

func TestParseVitestShapes(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "packages/react"), 0755)
	_ = os.WriteFile(filepath.Join(root, "packages/react/package.json"), []byte(`{"scripts":{"test:lib":"vitest","test:other":"vitest run && tsc"}}`), 0644)
	server := &graph.PackageNode{Name: "server", Path: "packages/server"}
	inv, ok := parseVitest(server, repoRootPrefix+"pnpm exec vitest run --passWithNoTests packages/server", root)
	if !ok || !inv.root || inv.filter != "packages/server" {
		t.Fatalf("root form: %+v %v", inv, ok)
	}
	if c := inv.command([]string{"packages/server/a.test.ts"}, "/r.json"); !strings.HasSuffix(c, "vitest run --passWithNoTests --reporter=default --reporter=json --outputFile.json='/r.json' 'packages/server/a.test.ts'") {
		t.Fatalf("narrowed: %s", c)
	}
	if c := inv.command(nil, "/r.json"); !strings.HasSuffix(c, " packages/server") {
		t.Fatalf("full run keeps the package filter: %s", c)
	}
	react := &graph.PackageNode{Name: "react", Path: "packages/react"}
	inv, ok = parseVitest(react, "pnpm run test:lib", root)
	if !ok || inv.root || inv.prefix != "pnpm exec vitest run" {
		t.Fatalf("script form: %+v %v", inv, ok)
	}
	if _, ok := parseVitest(react, "pnpm run test:other", root); ok {
		t.Fatalf("a script doing more than vitest is not narrowed")
	}
	if _, ok := parseVitest(server, repoRootPrefix+"pnpm exec vitest run --project x packages/server", root); ok {
		t.Fatalf("a flag value is not a package filter: not narrowed")
	}
}

func TestReadVitestReport(t *testing.T) {
	pkg := t.TempDir()
	report := filepath.Join(t.TempDir(), "r.json")
	_ = os.WriteFile(report, []byte(`{"testResults":[{"name":"`+filepath.Join(pkg, "src/a.test.ts")+`","status":"passed"},{"name":"`+filepath.Join(pkg, "src/b.test.ts")+`","status":"failed"},{"name":"/elsewhere/c.test.ts","status":"passed"}]}`), 0644)
	res, err := readVitestReport(report, pkg)
	if err != nil || len(res) != 2 || !res["src/a.test.ts"] || res["src/b.test.ts"] {
		t.Fatalf("got %v %v", res, err)
	}
}

// The agent's verify answers with the changed packages' failures without waiting for the
// dependents; they are left pending (a background run checks them).
func TestFailFastLeavesDependentsPending(t *testing.T) {
	root, marks := t.TempDir(), t.TempDir()
	for _, d := range []string{"core", "app"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0755)
	}
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "core", Path: "core", Language: graph.LangGeneric, TestCmd: "echo 'core.test.ts:1:1 - error TS1: broken'; exit 1"})
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangGeneric, Dependencies: []string{"core"}, TestCmd: "touch " + filepath.Join(marks, "app")})
	g.RebuildEdges()
	waited := false
	res := NewRunner(root).VerifyAffected(g, []string{"core/x"}, VerifyOptions{Strict: true, NoCache: true, FailFast: true, BeforeDependents: func() { waited = true }})
	if res.Status != "failed" || len(res.Pending) != 1 || waited {
		t.Fatalf("want core's failure with app pending: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(marks, "app")); err == nil {
		t.Fatalf("the dependent must not run in a fail-fast verify")
	}
}
