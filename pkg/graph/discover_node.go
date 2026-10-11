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
		// The package's own scripts are what its CI runs. Names vary across repos (TanStack:
		// test:lib / test:types / test:oxlint; others: check-types, test:unit), so take the
		// first conventional name present, run with the repo's package manager.
		var used []string // scripts chosen for the checks, for the runner's dependency builds
		script := func(names ...string) string {
			for _, s := range names {
				if _, ok := p.Scripts[s]; ok {
					used = append(used, s)
					return root.run + " " + s
				}
			}
			return ""
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
			TestCmd:      script(testScripts...),
			LintCmd:      script(lintScripts...),
			TypecheckCmd: script(typecheckScripts...),
		}
		node.PrepareCmd = root.prepare(name, used)
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

// Conventional script names per check, most specific first.
var (
	testScripts      = []string{"test", "test:unit", "test:lib", "tests"}
	lintScripts      = []string{"lint", "test:lint", "test:eslint", "test:oxlint", "eslint"}
	typecheckScripts = []string{"typecheck", "type-check", "check-types", "types", "test:types", "tsc"}
)

// rootToolchain describes workspace-level JS tools that can be scoped to one package.
type rootToolchain struct {
	nx        bool                // nx.json: package scripts are Nx targets
	nxDeps    map[string][]string // Nx targetDefaults dependsOn (string entries)
	turboDeps map[string][]string // turbo.json task ("typecheck", "pkg#build") -> dependsOn
	run       string              // package-manager script runner ("pnpm run", "yarn run", "bun run", "npm run")
	exec      string              // package-manager exec prefix ("pnpm exec", "yarn", "npx --no-install")
	test      string              // "vitest" | "jest" | ""
	lint      string              // "eslint" | ""
	tsc       bool
}

func rootRunners(root string) rootToolchain {
	tc := rootToolchain{run: "npm run", exec: "npx --no-install"}
	switch {
	case fileExistsAt(filepath.Join(root, "pnpm-lock.yaml")):
		tc.run, tc.exec = "pnpm run", "pnpm exec"
	case fileExistsAt(filepath.Join(root, "yarn.lock")):
		tc.run, tc.exec = "yarn run", "yarn"
	case fileExistsAt(filepath.Join(root, "bun.lockb")) || fileExistsAt(filepath.Join(root, "bun.lock")):
		tc.run, tc.exec = "bun run", "bunx"
	}
	var p npmPackageJSON
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	type taskDef struct {
		DependsOn []json.RawMessage `json:"dependsOn"`
	}
	depNames := func(defs map[string]taskDef) map[string][]string {
		out := map[string][]string{}
		for k, d := range defs {
			for _, raw := range d.DependsOn {
				var name string
				if json.Unmarshal(raw, &name) == nil { // object entries ({target, projects}) are skipped
					out[k] = append(out[k], name)
				}
			}
		}
		return out
	}
	if data, err := os.ReadFile(filepath.Join(root, "nx.json")); err == nil {
		var n struct {
			TargetDefaults map[string]taskDef `json:"targetDefaults"`
		}
		_ = json.Unmarshal(data, &n)
		tc.nx, tc.nxDeps = true, depNames(n.TargetDefaults)
	}
	if data, err := os.ReadFile(filepath.Join(root, "turbo.json")); err == nil {
		var t struct {
			Tasks    map[string]taskDef `json:"tasks"`
			Pipeline map[string]taskDef `json:"pipeline"` // turbo < 2
		}
		if json.Unmarshal(data, &t) == nil {
			tc.turboDeps = depNames(t.Tasks)
			for k, v := range depNames(t.Pipeline) {
				tc.turboDeps[k] = v
			}
		}
	}
	// A root tool can be scoped to a package only if the root really runs it repo-wide: a
	// config file at the root, or a root script invoking it directly. Merely depending on it
	// is not enough (TanStack Query lists vitest/eslint at the root but configures them per
	// package; scoping them from the root failed on a clean checkout).
	direct := func(tool string) bool {
		for _, v := range p.Scripts {
			for _, part := range strings.Split(v, "&&") {
				if f := strings.Fields(part); len(f) > 0 && f[0] == tool {
					return true
				}
			}
		}
		return false
	}
	hasConfig := func(globs ...string) bool {
		for _, gl := range globs {
			if m, _ := filepath.Glob(filepath.Join(root, gl)); len(m) > 0 {
				return true
			}
		}
		return false
	}
	switch {
	case direct("vitest") || hasConfig("vitest.config.*", "vitest.workspace.*"):
		tc.test = "vitest"
	case direct("jest") || hasConfig("jest.config.*"):
		tc.test = "jest"
	}
	if direct("eslint") || hasConfig("eslint.config.*", ".eslintrc*") {
		tc.lint = "eslint"
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
	tc.tsc = strings.Contains(all, "tsc") || strings.Contains(all, "typescript")
	return tc
}

// prepare returns the task-runner command that builds what the given scripts depend on in
// upstream packages (Nx targetDefaults / turbo.json "dependsOn": ["^build"]), or "" when the
// repo has no runner or the scripts need nothing built. Run directly, TanStack Query's type
// checks and trpc's example typechecks passed or failed depending on stale build output.
func (tc rootToolchain) prepare(pkg string, scripts []string) string {
	var targets []string
	seen := map[string]bool{}
	for _, s := range scripts {
		deps := tc.turboDeps[pkg+"#"+s]
		if deps == nil {
			deps = tc.turboDeps[s]
		}
		if tc.nx {
			deps = tc.nxDeps[s]
		}
		for _, d := range deps {
			if t, ok := strings.CutPrefix(d, "^"); ok && !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
		}
	}
	if len(targets) == 0 {
		return ""
	}
	if tc.nx {
		return "NX_DAEMON=false NX_NO_CLOUD=true " + tc.exec + " nx run-many -t " + strings.Join(targets, ",")
	}
	return tc.exec + " turbo run " + strings.Join(targets, " ") + " --output-logs=errors-only --log-prefix=none"
}

// scoped runs a root-level tool from the workspace root, limited to the package dir.
func (tc rootToolchain) scoped(tool, dir string) string {
	if tool == "" {
		return ""
	}
	args := dir
	if tool == "vitest" {
		args = "run --passWithNoTests " + dir
	} else if tool == "jest" {
		args = "--passWithNoTests " + dir
	}
	return `cd "$(git rev-parse --show-toplevel)" && ` + tc.exec + " " + tool + " " + args
}
