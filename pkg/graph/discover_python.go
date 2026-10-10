package graph

import (
	"path/filepath"
	"regexp"
	"strings"
)

// discoverPythonPackages registers Python projects (pyproject.toml or setup.py at any
// depth) and links PEP 621 / Poetry / uv / dependency-group requirements on other
// in-repo projects by normalised distribution name.
func (g *DependencyGraph) discoverPythonPackages(files []string) {
	type project struct {
		node *PackageNode
		deps []string
	}
	var projects []project
	byNorm := map[string]string{}
	reqName := regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)

	dirs := map[string]bool{}
	for _, base := range []string{"pyproject.toml", "setup.py"} {
		for _, d := range manifestDirs(files, base) {
			dirs[d] = true
		}
	}
	for dir := range dirs {
		sections := tomlSections(readFileString(filepath.Join(g.RootDir, dir, "pyproject.toml")))
		name := tomlString(sections["project"], "name")
		if name == "" {
			name = tomlString(sections["tool.poetry"], "name")
		}
		if name == "" {
			if dir == "." || !fileExistsAt(filepath.Join(g.RootDir, dir, "setup.py")) {
				continue // tooling-only pyproject (e.g. a repo-root lint config)
			}
			name = filepath.Base(dir)
		}
		reqs := tomlArray(sections["project"], "dependencies")
		for sec, lines := range sections {
			switch {
			case sec == "project.optional-dependencies" || sec == "dependency-groups":
				for _, k := range tomlKeys(lines) {
					reqs = append(reqs, tomlArray(lines, k)...)
				}
			case sec == "tool.poetry.dependencies" || strings.HasPrefix(sec, "tool.poetry.group.") || sec == "tool.uv.sources":
				reqs = append(reqs, tomlKeys(lines)...)
			}
		}
		var deps []string
		for _, r := range reqs {
			if m := reqName.FindStringSubmatch(r); m != nil {
				deps = append(deps, normalizePyName(m[1]))
			}
		}
		byNorm[normalizePyName(name)] = name
		projects = append(projects, project{node: &PackageNode{
			Name:     name,
			Path:     dir,
			Language: LangPython,
			Version:  orDefault(tomlString(sections["project"], "version"), "1.0.0"),
			TestCmd:  "python -m pytest -q",
			LintCmd:  "ruff check .",
		}, deps: deps})
	}
	inRepo := map[string]bool{}
	for _, n := range byNorm {
		inRepo[n] = true
	}
	for _, p := range projects {
		var named []string
		for _, d := range p.deps {
			if n, ok := byNorm[d]; ok {
				named = append(named, n)
			}
		}
		p.node.Dependencies = inRepoDeps(named, inRepo, p.node.Name)
		g.AddPackage(p.node)
	}
}

var pyNameSep = regexp.MustCompile(`[-_.]+`)

// normalizePyName applies PEP 503 name normalisation.
func normalizePyName(n string) string {
	return pyNameSep.ReplaceAllString(strings.ToLower(n), "-")
}
