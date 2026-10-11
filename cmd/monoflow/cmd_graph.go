package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/verify"
)

func handleGraph(rootDir string) {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		printRaw(fmt.Sprintf(`{"error": %q}`, err))
		os.Exit(1)
	}

	if len(os.Args) == 2 {
		// Output full graph in JSON
		pkgs := g.GetAllPackages()
		type nodeOutput struct {
			Name         string   `json:"name"`
			Path         string   `json:"path"`
			Language     string   `json:"language"`
			Upstream     []string `json:"upstream"`
			Downstream   []string `json:"downstream"`
			TestCmd      string   `json:"test_cmd,omitempty"`
			LintCmd      string   `json:"lint_cmd,omitempty"`
			TypecheckCmd string   `json:"typecheck_cmd,omitempty"`
		}
		nodes := make([]nodeOutput, len(pkgs))
		for i, p := range pkgs {
			nodes[i] = nodeOutput{
				Name:         p.Name,
				Path:         p.Path,
				Language:     string(p.Language),
				Upstream:     g.GetUpstream(p.Name),
				Downstream:   g.GetDownstream(p.Name),
				TestCmd:      p.TestCmd,
				LintCmd:      p.LintCmd,
				TypecheckCmd: p.TypecheckCmd,
			}
		}
		printOut(map[string]any{
			"package_count": len(nodes),
			"packages":      nodes,
		})
		return
	}

	sub := os.Args[2]
	switch sub {
	case "query":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing package name for query"}`)
			os.Exit(1)
		}
		pkgName := os.Args[3]
		pkg, exists := resolvePackageArg(g, rootDir, pkgName)
		if !exists {
			printRaw(fmt.Sprintf(`{"error": "package '%s' not found (use a name from 'monoflow graph' or a path inside a package)"}`, pkgName))
			os.Exit(1)
		}
		out := map[string]any{
			"package":           pkg.Name,
			"path":              pkg.Path,
			"depends_on":        g.GetUpstream(pkg.Name),
			"dependents":        len(g.GetTransitiveDownstream(pkg.Name)),
			"direct_dependents": g.GetDownstream(pkg.Name),
		}
		if len(pkg.PinnedDependencies) > 0 {
			out["pinned_dependencies"] = pkg.PinnedDependencies
		}
		if pinned := pinnedDependents(g, pkg.Name); len(pinned) > 0 {
			out["pinned_dependents"] = pinned
		}
		// For a Go package directory: who imports it, package by package, across all modules
		// (what agents otherwise grep the whole repo for).
		if pkg.Language == graph.LangGo {
			dir := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(pkgName)), "./")
			if fi, err := os.Stat(filepath.Join(rootDir, dir)); err != nil || !fi.IsDir() {
				dir = pkg.Path
			}
			if imp, byModule, ok := verify.GoImporters(rootDir, g, dir); ok {
				n := 0
				for _, v := range byModule {
					n += len(v)
				}
				out["go_package"] = imp
				out["imported_by"] = byModule // module dir -> packages importing it (code or tests)
				out["imported_by_count"] = n
				delete(out, "direct_dependents")
			}
		}
		printOut(out)

	case "impact":
		if len(os.Args) < 4 {
			printRaw(`{"error": "missing file path for impact analysis"}`)
			os.Exit(1)
		}
		filePath := os.Args[3]
		pkg := g.MapFileToPackage(filePath)
		if pkg == nil {
			printRaw(fmt.Sprintf(`{"file": %q, "impact": "none (untracked/root file)"}`, filePath))
			return
		}
		// Compact for agents: one dependents list (direct ones are a subset of it).
		deps := g.GetTransitiveDownstream(pkg.Name)
		out := map[string]any{
			"package":       pkg.Name,
			"dependents":    len(deps),
			"direct":        len(g.GetDownstream(pkg.Name)),
			"verify_covers": "monoflow verify tests this package and all of these",
		}
		if len(deps) > 30 {
			out["dependents_first_30"] = deps[:30]
		} else if len(deps) > 0 {
			out["dependent_packages"] = deps
		}
		if pinned := pinnedDependents(g, pkg.Name); len(pinned) > 0 {
			out["pinned_dependents"] = pinned
			out["pinned_note"] = "they build against a release of this package: unaffected now, notified on its next release"
		}
		printOut(out)

	case "affected":
		changedFiles := getGitChangedFiles(rootDir)
		affected := g.ComputeAffected(changedFiles)
		printOut(affected)
	}
}

// pinnedDependents lists consumers that pin a release of pkg (they are not affected by
// changes to its live code but must know about breaking releases).
func pinnedDependents(g *graph.DependencyGraph, pkg string) []string {
	out := []string{}
	for _, p := range g.GetAllPackages() {
		if containsStr(p.PinnedDependencies, pkg) {
			out = append(out, p.Name)
		}
	}
	return out
}
