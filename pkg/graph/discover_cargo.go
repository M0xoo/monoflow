package graph

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// discoverCargoPackages registers every crate (Cargo.toml with a [package]) and links
// [dependencies]/[dev-dependencies]/[build-dependencies] (incl. target-specific, table
// form and renamed ones) on other in-repo crates.
func (g *DependencyGraph) discoverCargoPackages(files []string) {
	type crate struct {
		node *PackageNode
		deps []string
	}
	var crates []crate
	names := map[string]bool{}
	renamed := regexp.MustCompile(`package\s*=\s*["']([^"']+)["']`)
	for _, dir := range manifestDirs(files, "Cargo.toml") {
		sections := tomlSections(readFileString(filepath.Join(g.RootDir, dir, "Cargo.toml")))
		pkg, ok := sections["package"]
		if !ok {
			continue // virtual workspace manifest
		}
		name := tomlString(pkg, "name")
		if name == "" {
			continue
		}
		var deps []string
		for sec, lines := range sections {
			last := sec[strings.LastIndex(sec, ".")+1:]
			switch {
			case last == "dependencies" || last == "dev-dependencies" || last == "build-dependencies":
				deps = append(deps, tomlKeys(lines)...)
				for _, l := range lines {
					if m := renamed.FindStringSubmatch(l); m != nil {
						deps = append(deps, m[1])
					}
				}
			case strings.Contains(sec, "dependencies."):
				d := last // [dependencies.foo] table form
				if r := tomlString(lines, "package"); r != "" {
					d = r
				}
				deps = append(deps, d)
			}
		}
		names[name] = true
		crates = append(crates, crate{node: &PackageNode{
			Name:         name,
			Path:         dir,
			Language:     LangRust,
			Version:      orDefault(tomlString(pkg, "version"), "1.0.0"),
			TestCmd:      "cargo test -p " + name,
			LintCmd:      "cargo clippy -p " + name + " -- -D warnings",
			TypecheckCmd: "cargo check -p " + name,
		}, deps: deps})
	}
	for _, c := range crates {
		c.node.Dependencies = inRepoDeps(c.deps, names, c.node.Name)
		g.AddPackage(c.node)
	}
}

// inRepoDeps keeps the unique dependency names that are packages of this repo.
func inRepoDeps(deps []string, inRepo map[string]bool, self string) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, d := range deps {
		if inRepo[d] && d != self && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
