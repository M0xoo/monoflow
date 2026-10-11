package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

// A change to lib/codes reaches app/uses (imports it) and app/tested (only its tests import it),
// but not app/other: only the first two run, and gofmt sees only the changed files.
func TestGoScopedCommandsRunOnlyImportingPackages(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0755)
		if err := os.WriteFile(p, []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("lib/go.mod", "module example.com/lib\n\ngo 1.21\n")
	write("lib/codes/codes.go", "package codes\n\nconst Ok = \"OK\"\n")
	write("app/go.mod", "module example.com/app\n\ngo 1.21\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n")
	write("app/uses/uses.go", "package uses\n\nimport \"example.com/lib/codes\"\n\nvar V = codes.Ok\n")
	write("app/tested/tested.go", "package tested\n\nvar V = 1\n")
	write("app/tested/tested_test.go", "package tested_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/lib/codes\"\n)\n\nfunc TestX(t *testing.T) { _ = codes.Ok }\n")
	write("app/other/other.go", "package other\n\nvar V = 2\n")

	app := &graph.PackageNode{Name: "example.com/app", Path: "app", Language: graph.LangGo,
		TestCmd: "go test ./...", LintCmd: graph.GoLintCmd, TypecheckCmd: "go build ./..."}
	cmds, skip, ok := goScopedCommands(root, app, []string{"lib/codes/codes.go"})
	if !ok || skip {
		t.Fatalf("want narrowed commands, got ok=%v skip=%v", ok, skip)
	}
	all := strings.Join(cmds, " ; ")
	if !strings.Contains(all, "example.com/app/uses") || !strings.Contains(all, "example.com/app/tested") || strings.Contains(all, "example.com/app/other") {
		t.Fatalf("wrong package selection: %s", all)
	}
	if strings.Contains(all, "gofmt") {
		t.Fatalf("no changed file in app: no gofmt step expected: %s", all)
	}

	lib := &graph.PackageNode{Name: "example.com/lib", Path: "lib", Language: graph.LangGo,
		TestCmd: "go test ./...", LintCmd: graph.GoLintCmd, TypecheckCmd: "go build ./..."}
	cmds, _, _ = goScopedCommands(root, lib, []string{"lib/codes/codes.go"})
	if all = strings.Join(cmds, " ; "); !strings.Contains(all, "gofmt -l codes/codes.go") {
		t.Fatalf("gofmt must check the changed file only: %s", all)
	}

	if _, _, ok := goScopedCommands(root, app, []string{"lib/codes/codes.go", "lib/CHANGELOG.md"}); !ok {
		t.Fatalf("a CHANGELOG edit must not disable scoping")
	}
	if _, _, ok := goScopedCommands(root, app, []string{"lib/go.mod"}); ok {
		t.Fatalf("a go.mod change must not be narrowed")
	}

	// graph query lib/codes: who imports it, package by package, across modules.
	g := graph.New(root)
	g.AddPackage(lib)
	g.AddPackage(app)
	imp, by, ok := GoImporters(root, g, "lib/codes")
	if !ok || imp != "example.com/lib/codes" || strings.Join(by["app"], ",") != "app/tested,app/uses" {
		t.Fatalf("importers: %s %v", imp, by)
	}
}
