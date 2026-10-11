package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
)

// Package-level affected testing for Go. A module "depends on" a change when one of its
// packages imports a changed package, but `go test ./...` in that module also runs every
// package that does not: in opentelemetry-go a change to otel/codes made 27 modules re-run all
// their packages. Here each affected module runs only the packages whose build or tests import a
// changed package (directly or not, per `go list -test -deps`), and gofmt looks only at the
// changed files. That is sound for Go (a package that does not import the change cannot be
// broken by it) and is analysis an agent with plain git would not do by hand.

// goScopedCommands returns the narrowed commands for a Go module given the changed files, or
// ok=false to keep the module's own commands (custom commands, or a change that is not plain
// Go source: go.mod, embedded files, testdata). skip=true means no package of the module
// imports the change, so nothing can break.
func goScopedCommands(root string, pkg *graph.PackageNode, changed []string) (cmds []string, skip, ok bool) {
	if pkg.Language != graph.LangGo || pkg.TestCmd != "go test ./..." || pkg.LintCmd != graph.GoLintCmd ||
		(pkg.TypecheckCmd != "" && pkg.TypecheckCmd != "go build ./...") {
		return nil, false, false
	}
	changedImports, ownFiles, plain := goChangedImports(root, pkg, changed)
	if !plain || len(changedImports) == 0 {
		return nil, false, false
	}
	moduleDir := filepath.Join(root, pkg.Path)
	selected, err := goPackagesImporting(moduleDir, changedImports)
	if err != nil {
		return nil, false, false
	}
	if len(selected) == 0 && len(ownFiles) == 0 {
		return nil, true, true
	}
	if len(ownFiles) > 0 {
		cmds = append(cmds, "unformatted=$(gofmt -l "+strings.Join(ownFiles, " ")+"); if [ -n \"$unformatted\" ]; then echo \"gofmt needed (run gofmt -w):\"; echo \"$unformatted\"; exit 1; fi")
	}
	if len(selected) > 0 {
		list := strings.Join(selected, " ")
		cmds = append(cmds, "go vet "+list, "go test "+list)
	}
	return cmds, false, true
}

// goChangedImports maps changed .go files to the import paths of their packages (any module),
// and lists the changed .go files inside pkg's own module (for gofmt). plain=false when a
// changed file in a Go module is not .go source: its effect is not visible to `go list`.
func goChangedImports(root string, pkg *graph.PackageNode, changed []string) (imports []string, ownFiles []string, plain bool) {
	seen := map[string]bool{}
	for _, f := range changed {
		f = filepath.ToSlash(f)
		modDir, modPath := goModuleOf(root, filepath.Dir(f))
		if modDir == "" {
			continue // not in a Go module
		}
		if !strings.HasSuffix(f, ".go") {
			return nil, nil, false
		}
		rel, _ := filepath.Rel(filepath.Join(root, modDir), filepath.Join(root, filepath.Dir(f)))
		imp := modPath
		if rel = filepath.ToSlash(rel); rel != "." {
			imp = modPath + "/" + rel
		}
		if !seen[imp] {
			seen[imp] = true
			imports = append(imports, imp)
		}
		if modDir == filepath.ToSlash(filepath.Clean(pkg.Path)) {
			if _, err := os.Stat(filepath.Join(root, f)); err == nil {
				r, _ := filepath.Rel(modDir, f)
				ownFiles = append(ownFiles, filepath.ToSlash(r))
			}
		}
	}
	return imports, ownFiles, true
}

// goModuleOf finds the nearest enclosing go.mod of dir (relative to root): its dir and module path.
func goModuleOf(root, dir string) (string, string) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if data, err := os.ReadFile(filepath.Join(root, d, "go.mod")); err == nil {
			for _, l := range strings.Split(string(data), "\n") {
				if f := strings.Fields(l); len(f) == 2 && f[0] == "module" {
					return filepath.ToSlash(d), strings.Trim(f[1], `"`)
				}
			}
			return "", ""
		}
		if d == "." || d == "/" || d == "" {
			return "", ""
		}
	}
}

// goPackagesImporting lists the packages of the module in dir whose build or tests depend on
// any of the given import paths (or are one of them).
func goPackagesImporting(dir string, imports []string) ([]string, error) {
	cmd := exec.Command("go", "list", "-e", "-test", "-deps=false", "-f", `{{.ImportPath}}{{range .Deps}} {{.}}{{end}}`, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, i := range imports {
		want[i] = true
	}
	sel := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		// Test variants print as "p [p.test]" (Deps then include test deps) or "p.test".
		base := f[0]
		deps := f[1:]
		if len(f) > 1 && strings.HasPrefix(f[1], "[") {
			deps = f[2:]
		}
		if strings.HasSuffix(base, ".test") { // the generated test main
			continue
		}
		base = strings.TrimSuffix(base, "_test") // external tests (package p_test) test p
		hit := want[base]
		for _, d := range deps {
			if hit {
				break
			}
			hit = want[d]
		}
		if hit {
			sel[base] = true
		}
	}
	var res []string
	for p := range sel {
		res = append(res, p)
	}
	sort.Strings(res)
	return res, nil
}
