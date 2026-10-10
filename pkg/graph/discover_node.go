package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type npmPackageJSON struct {
	Name             string            `json:"name"`
	Version          string            `json:"version,omitempty"`
	Workspaces       any               `json:"workspaces"`
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
	Scripts          map[string]string `json:"scripts"`
}

// discoverNodeWorkspaces registers JS/TS workspace packages (npm/yarn/pnpm/bun workspace
// globs, including "**" and "!" exclusions), linking dependencies on other workspace
// packages by name.
func (g *DependencyGraph) discoverNodeWorkspaces(files []string) {
	patterns := nodeWorkspacePatterns(g.RootDir)
	if len(patterns) == 0 {
		patterns = []string{"services/*", "packages/*", "apps/*", "libs/*"}
	}

	rootTS := fileExistsAt(filepath.Join(g.RootDir, "tsconfig.json")) || fileExistsAt(filepath.Join(g.RootDir, "tsconfig.base.json"))
	root := rootRunners(g.RootDir)
	for _, dir := range manifestDirs(files, "package.json") {
		if dir == "." || !matchesWorkspace(dir, patterns) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(g.RootDir, dir, "package.json"))
		if err != nil {
			continue
		}
		var p npmPackageJSON
		if json.Unmarshal(data, &p) != nil {
			continue
		}
		name := p.Name
		if name == "" {
			name = filepath.Base(dir)
		}
		var deps []string
		for _, m := range []map[string]string{p.Dependencies, p.DevDependencies, p.PeerDependencies} {
			for d := range m {
				deps = append(deps, d)
			}
		}
		script := func(s string) string {
			if _, ok := p.Scripts[s]; !ok {
				return ""
			}
			if s == "test" {
				return "npm test"
			}
			return "npm run " + s
		}
		lang := LangJavaScript
		if isTypeScriptPackage(filepath.Join(g.RootDir, dir), p, rootTS) {
			lang = LangTypeScript
		}
		node := &PackageNode{
			Name:         name,
			Path:         dir,
			Language:     lang,
			Version:      orDefault(p.Version, "1.0.0"),
			Dependencies: deps,
			TestCmd:      script("test"),
			LintCmd:      script("lint"),
			TypecheckCmd: script("typecheck"),
		}
		// Many monorepos run vitest/jest/eslint/tsc once from the root; scope those
		// root tools to this package instead of leaving it unverified.
		if node.TestCmd == "" {
			node.TestCmd = root.scoped(root.test, dir)
		}
		if node.LintCmd == "" {
			node.LintCmd = root.scoped(root.lint, dir)
		}
		if node.TypecheckCmd == "" && root.tsc {
			if fileExistsAt(filepath.Join(g.RootDir, dir, "tsconfig.json")) {
				node.TypecheckCmd = root.exec + " tsc --noEmit -p tsconfig.json"
			} else if lang == LangTypeScript && rootTS {
				// Typed through the root tsconfig: one repo-wide check, run once per verify
				// (the runner deduplicates repo-root commands across packages).
				node.TypecheckCmd = `cd "$(git rev-parse --show-toplevel)" && ` + root.exec + " tsc --noEmit"
			}
		}
		g.AddPackage(node)
	}
}

// nodeWorkspacePatterns reads workspace globs from package.json and pnpm-workspace.yaml.
func nodeWorkspacePatterns(root string) []string {
	var patterns []string
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var rootPkg npmPackageJSON
		if json.Unmarshal(data, &rootPkg) == nil {
			var list []any
			switch w := rootPkg.Workspaces.(type) {
			case []any:
				list = w
			case map[string]any:
				list, _ = w["packages"].([]any)
			}
			for _, v := range list {
				if s, ok := v.(string); ok {
					patterns = append(patterns, s)
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
		inPackages := false
		for _, l := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(t, "packages:"):
				inPackages = true
			case inPackages && strings.HasPrefix(t, "-"):
				if pat := strings.Trim(strings.TrimPrefix(t, "-"), " '\"\t"); pat != "" {
					patterns = append(patterns, pat)
				}
			case inPackages && t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(l, " "):
				inPackages = false
			}
		}
	}
	return patterns
}

func matchesWorkspace(dir string, patterns []string) bool {
	matched := false
	for _, pat := range patterns {
		neg := strings.HasPrefix(pat, "!")
		if globToRegexp(strings.TrimPrefix(pat, "!")).MatchString(dir) {
			matched = !neg
		}
	}
	return matched
}

// isTypeScriptPackage reports whether a JS workspace package is written in TypeScript
// (own tsconfig, a typescript dependency, or TS sources under a root tsconfig).
func isTypeScriptPackage(dir string, p npmPackageJSON, rootTS bool) bool {
	if fileExistsAt(filepath.Join(dir, "tsconfig.json")) {
		return true
	}
	for _, m := range []map[string]string{p.DevDependencies, p.Dependencies} {
		if _, ok := m["typescript"]; ok {
			return true
		}
	}
	if rootTS {
		for _, src := range []string{"src/index.ts", "src/index.tsx", "index.ts", "src/main.ts"} {
			if fileExistsAt(filepath.Join(dir, src)) {
				return true
			}
		}
	}
	return false
}

func fileExistsAt(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// rootToolchain describes workspace-level JS tools that can be scoped to one package.
type rootToolchain struct {
	exec string // package-manager exec prefix ("pnpm exec", "yarn", "npx --no-install")
	test string // "vitest" | "jest" | ""
	lint string // "eslint" | ""
	tsc  bool
}

func rootRunners(root string) rootToolchain {
	tc := rootToolchain{exec: "npx --no-install"}
	switch {
	case fileExistsAt(filepath.Join(root, "pnpm-lock.yaml")):
		tc.exec = "pnpm exec"
	case fileExistsAt(filepath.Join(root, "yarn.lock")):
		tc.exec = "yarn"
	case fileExistsAt(filepath.Join(root, "bun.lockb")) || fileExistsAt(filepath.Join(root, "bun.lock")):
		tc.exec = "bunx"
	}
	var p npmPackageJSON
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	all := ""
	for _, v := range p.Scripts {
		all += " " + v
	}
	for _, m := range []map[string]string{p.DevDependencies, p.Dependencies} {
		for d := range m {
			all += " " + d
		}
	}
	switch {
	case strings.Contains(all, "vitest"):
		tc.test = "vitest"
	case strings.Contains(all, "jest"):
		tc.test = "jest"
	}
	if strings.Contains(all, "eslint") {
		tc.lint = "eslint"
	}
	tc.tsc = strings.Contains(all, "tsc") || strings.Contains(all, "typescript")
	return tc
}

// scoped runs a root-level tool from the workspace root, limited to the package dir.
func (tc rootToolchain) scoped(tool, dir string) string {
	if tool == "" {
		return ""
	}
	args := dir
	if tool == "vitest" {
		args = "run " + dir
	} else if tool == "jest" {
		args = "--passWithNoTests " + dir
	}
	return `cd "$(git rev-parse --show-toplevel)" && ` + tc.exec + " " + tool + " " + args
}
