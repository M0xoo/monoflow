package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/antigravity/monoflow/pkg/graph"
)

func handleGraph(rootDir string) {
	g, err := graph.LoadOrDiscover(rootDir)
	if err != nil {
		fmt.Printf(`{"error": "%s"}`+"\n", err)
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
		data, _ := json.MarshalIndent(map[string]any{
			"package_count": len(nodes),
			"packages":      nodes,
		}, "", "  ")
		fmt.Println(string(data))
		return
	}

	sub := os.Args[2]
	switch sub {
	case "query":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing package name for query"}`)
			os.Exit(1)
		}
		pkgName := os.Args[3]
		pkg, exists := resolvePackageArg(g, rootDir, pkgName)
		if !exists {
			fmt.Printf(`{"error": "package '%s' not found (use a name from 'monoflow graph' or a path inside a package)"}`+"\n", pkgName)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(map[string]any{
			"package":               pkg.Name,
			"path":                  pkg.Path,
			"language":              pkg.Language,
			"upstream_dependencies": g.GetUpstream(pkg.Name),
			"pinned_dependencies":   pkg.PinnedDependencies,
			"downstream_dependents": g.GetDownstream(pkg.Name),
			"transitive_dependents": g.GetTransitiveDownstream(pkg.Name),
			"pinned_dependents":     pinnedDependents(g, pkg.Name),
		}, "", "  ")
		fmt.Println(string(data))

	case "impact":
		if len(os.Args) < 4 {
			fmt.Println(`{"error": "missing file path for impact analysis"}`)
			os.Exit(1)
		}
		filePath := os.Args[3]
		pkg := g.MapFileToPackage(filePath)
		if pkg == nil {
			fmt.Printf(`{"file": "%s", "impact": "none (untracked/root file)"}`+"\n", filePath)
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
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))

	case "affected":
		changedFiles := getGitChangedFiles(rootDir)
		affected := g.ComputeAffected(changedFiles)
		data, _ := json.MarshalIndent(affected, "", "  ")
		fmt.Println(string(data))
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
