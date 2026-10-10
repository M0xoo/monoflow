package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AutoDiscover scans the workspace filesystem and registers discovered packages.
func (g *DependencyGraph) AutoDiscover() error {
	// 1. Node / NPM / pnpm / Turbo / Nx workspaces
	g.discoverNodeWorkspaces()

	// 2. Cargo workspaces
	g.discoverCargoWorkspaces()

	// 3. Go workspaces / multi-module
	g.discoverGoModules()

	// 4. Python packages
	g.discoverPythonPackages()

	// 5. Contracts & Schemas (Proto / OpenAPI)
	g.discoverContracts()

	// 6. Fallback directory heuristics if empty
	if len(g.packages) == 0 {
		g.discoverByDirectoryHeuristic()
	}

	g.rebuildEdges()
	return nil
}

type npmPackageJSON struct {
	Name            string            `json:"name"`
	Version         string            `json:"version,omitempty"`
	Workspaces      any               `json:"workspaces"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Scripts         map[string]string `json:"scripts"`
}

func (g *DependencyGraph) discoverNodeWorkspaces() {
	rootPkgPath := filepath.Join(g.RootDir, "package.json")
	var patterns []string

	if data, err := os.ReadFile(rootPkgPath); err == nil {
		var rootPkg npmPackageJSON
		if err := json.Unmarshal(data, &rootPkg); err == nil {
			if wList, ok := rootPkg.Workspaces.([]any); ok {
				for _, w := range wList {
					if s, ok := w.(string); ok {
						patterns = append(patterns, s)
					}
				}
			} else if wMap, ok := rootPkg.Workspaces.(map[string]any); ok {
				if pkgs, ok := wMap["packages"].([]any); ok {
					for _, w := range pkgs {
						if s, ok := w.(string); ok {
							patterns = append(patterns, s)
						}
					}
				}
			}
		}
	}

	// pnpm-workspace.yaml
	pnpmPath := filepath.Join(g.RootDir, "pnpm-workspace.yaml")
	if data, err := os.ReadFile(pnpmPath); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "-") {
				pat := strings.Trim(strings.TrimPrefix(trimmed, "-"), " '\"\t")
				if pat != "" {
					patterns = append(patterns, pat)
				}
			}
		}
	}

	if len(patterns) == 0 {
		patterns = []string{"services/*", "packages/*", "apps/*", "libs/*"}
	}

	// Expand patterns
	for _, pat := range patterns {
		g.scanGlobPattern(pat, func(dir string) {
			pkgJson := filepath.Join(dir, "package.json")
			if data, err := os.ReadFile(pkgJson); err == nil {
				var p npmPackageJSON
				if err := json.Unmarshal(data, &p); err == nil {
					name := p.Name
					if name == "" {
						name = filepath.Base(dir)
					}
					deps := make([]string, 0)
					for d := range p.Dependencies {
						deps = append(deps, d)
					}
					for d := range p.DevDependencies {
						deps = append(deps, d)
					}

					testCmd := "npm test"
					lintCmd := "npm run lint"
					typeCmd := "npm run typecheck"
					if p.Scripts != nil {
						if _, ok := p.Scripts["test"]; !ok {
							testCmd = ""
						}
						if _, ok := p.Scripts["lint"]; !ok {
							lintCmd = ""
						}
						if _, ok := p.Scripts["typecheck"]; !ok {
							typeCmd = ""
						}
					}

					ver := p.Version
					if ver == "" {
						ver = "1.0.0"
					}

					lang := LangJavaScript
					if isTypeScriptPackage(dir, p) {
						lang = LangTypeScript
					}
					rel, _ := filepath.Rel(g.RootDir, dir)
					g.AddPackage(&PackageNode{
						Name:         name,
						Path:         rel,
						Language:     lang,
						Version:      ver,
						Dependencies: deps,
						TestCmd:      testCmd,
						LintCmd:      lintCmd,
						TypecheckCmd: typeCmd,
					})
				}
			}
		})
	}
}

// isTypeScriptPackage reports whether a JS workspace package is written in TypeScript.
func isTypeScriptPackage(dir string, p npmPackageJSON) bool {
	if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err == nil {
		return true
	}
	if _, ok := p.DevDependencies["typescript"]; ok {
		return true
	}
	_, ok := p.Dependencies["typescript"]
	return ok
}

func (g *DependencyGraph) discoverCargoWorkspaces() {
	cargoPath := filepath.Join(g.RootDir, "Cargo.toml")
	data, err := os.ReadFile(cargoPath)
	if err != nil {
		return
	}
	content := string(data)
	if !strings.Contains(content, "[workspace]") {
		return
	}

	// Search for members
	lines := strings.Split(content, "\n")
	inMembers := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "members") {
			inMembers = true
			continue
		}
		if inMembers {
			if strings.Contains(trimmed, "]") {
				inMembers = false
			}
			raw := strings.Trim(trimmed, "[], '\"\t")
			if raw != "" {
				g.scanGlobPattern(raw, func(dir string) {
					if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil {
						name := filepath.Base(dir)
						rel, _ := filepath.Rel(g.RootDir, dir)
						g.AddPackage(&PackageNode{
							Name:         name,
							Path:         rel,
							Language:     LangRust,
							Version:      "1.0.0",
							TestCmd:      "cargo test -p " + name,
							LintCmd:      "cargo clippy -p " + name,
							TypecheckCmd: "cargo check -p " + name,
						})
					}
				})
			}
		}
	}
}

func (g *DependencyGraph) discoverGoModules() {
	// Candidate module dirs: go.work "use" entries plus any go.mod one level below a top-level dir.
	dirs := map[string]bool{}
	for _, rel := range parseGoWorkUses(filepath.Join(g.RootDir, "go.work")) {
		dirs[filepath.Clean(rel)] = true
	}
	entries, err := os.ReadDir(g.RootDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		subEntries, _ := os.ReadDir(filepath.Join(g.RootDir, entry.Name()))
		for _, sub := range subEntries {
			if sub.IsDir() {
				dirs[filepath.Join(entry.Name(), sub.Name())] = true
			}
		}
	}

	var found []*PackageNode
	for rel := range dirs {
		if _, err := os.Stat(filepath.Join(g.RootDir, rel, "go.mod")); err != nil {
			continue
		}
		found = append(found, &PackageNode{
			Name:         filepath.ToSlash(rel),
			Path:         rel,
			Language:     LangGo,
			Version:      "1.0.0",
			TestCmd:      "go test ./...",
			LintCmd:      goLintCmd,
			TypecheckCmd: "go build ./...",
		})
	}
	resolveGoDependencies(g.RootDir, found)
	for _, p := range found {
		g.AddPackage(p)
	}
}

// goLintCmd fails on unformatted files (naming them) as well as vet findings, so agents
// never need raw gofmt/go vet to match what CI enforces.
const goLintCmd = `unformatted=$(gofmt -l .); if [ -n "$unformatted" ]; then echo "gofmt needed (run gofmt -w):"; echo "$unformatted"; exit 1; fi; go vet ./...`

// goModInfo is the subset of a go.mod file needed to link in-repo modules.
type goModInfo struct {
	Module   string
	Requires []string
	Replaces map[string]string // module path -> local replacement dir (relative to the go.mod)
}

func parseGoMod(path string) (goModInfo, bool) {
	info := goModInfo{Replaces: map[string]string{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return info, false
	}
	block := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if line == ")" {
			block = ""
			continue
		}
		fields := strings.Fields(line)
		switch {
		case fields[0] == "module" && len(fields) > 1:
			info.Module = strings.Trim(fields[1], `"`)
			continue
		case (fields[0] == "require" || fields[0] == "replace") && len(fields) > 1 && fields[1] == "(":
			block = fields[0]
			continue
		case fields[0] == "require" || fields[0] == "replace":
			block, fields = fields[0], fields[1:]
			parseGoModDirective(&info, block, fields)
			block = ""
			continue
		}
		if block != "" {
			parseGoModDirective(&info, block, fields)
		}
	}
	return info, info.Module != ""
}

func parseGoModDirective(info *goModInfo, kind string, fields []string) {
	if len(fields) == 0 {
		return
	}
	switch kind {
	case "require":
		info.Requires = append(info.Requires, strings.Trim(fields[0], `"`))
	case "replace":
		for i, f := range fields {
			if f == "=>" && i+1 < len(fields) {
				target := fields[i+1]
				if strings.HasPrefix(target, ".") || strings.HasPrefix(target, "/") {
					info.Replaces[strings.Trim(fields[0], `"`)] = target
				}
			}
		}
	}
}

func parseGoWorkUses(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var uses []string
	inUse := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "use (":
			inUse = true
		case inUse && line == ")":
			inUse = false
		case inUse && line != "":
			uses = append(uses, strings.Trim(line, `"`))
		case strings.HasPrefix(line, "use "):
			uses = append(uses, strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "use ")), `"`))
		}
	}
	return uses
}

// resolveGoDependencies links Go packages through their go.mod require/replace directives,
// matching either the required module path or a local replace directory to an in-repo module.
// pkgs is the full set of in-repo Go modules; when targets is given only those are updated.
func resolveGoDependencies(rootDir string, pkgs []*PackageNode, targets ...*PackageNode) {
	byModule := map[string]string{}
	byDir := map[string]string{}
	infos := map[*PackageNode]goModInfo{}
	for _, p := range pkgs {
		info, ok := parseGoMod(filepath.Join(rootDir, p.Path, "go.mod"))
		if !ok {
			continue
		}
		infos[p] = info
		byModule[info.Module] = p.Name
		byDir[filepath.Clean(p.Path)] = p.Name
	}
	if len(targets) == 0 {
		targets = pkgs
	}
	for _, p := range targets {
		info, ok := infos[p]
		if !ok {
			continue
		}
		seen := map[string]bool{}
		deps := make([]string, 0)
		add := func(name string) {
			if name != "" && name != p.Name && !seen[name] {
				seen[name] = true
				deps = append(deps, name)
			}
		}
		for _, req := range info.Requires {
			if target, ok := info.Replaces[req]; ok {
				add(byDir[filepath.Clean(filepath.Join(p.Path, target))])
			}
			add(byModule[req])
		}
		sort.Strings(deps)
		p.Dependencies = deps
	}
}

func (g *DependencyGraph) discoverPythonPackages() {
	candidateRoots := []string{"services", "packages", "apps", "libs"}
	for _, root := range candidateRoots {
		abs := filepath.Join(g.RootDir, root)
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			target := filepath.Join(abs, e.Name())
			isPy := false
			for _, check := range []string{"pyproject.toml", "requirements.txt", "setup.py"} {
				if _, err := os.Stat(filepath.Join(target, check)); err == nil {
					isPy = true
					break
				}
			}
			if isPy {
				name := root + "/" + e.Name()
				rel, _ := filepath.Rel(g.RootDir, target)
				g.AddPackage(&PackageNode{
					Name:         name,
					Path:         rel,
					Language:     LangPython,
					Version:      "1.0.0",
					TestCmd:      "pytest",
					LintCmd:      "ruff check .",
					TypecheckCmd: "mypy .",
				})
			}
		}
	}
}

func (g *DependencyGraph) discoverContracts() {
	contractsDir := filepath.Join(g.RootDir, "contracts")
	if fi, err := os.Stat(contractsDir); err == nil && fi.IsDir() {
		rel, _ := filepath.Rel(g.RootDir, contractsDir)
		g.AddPackage(&PackageNode{
			Name:     "contracts",
			Path:     rel,
			Language: LangProto,
			Version:  "1.0.0",
		})
	}
}

func (g *DependencyGraph) discoverByDirectoryHeuristic() {
	candidateRoots := []string{"services", "packages", "apps", "libs", "components"}
	for _, root := range candidateRoots {
		abs := filepath.Join(g.RootDir, root)
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				name := root + "/" + e.Name()
				rel, _ := filepath.Rel(g.RootDir, filepath.Join(abs, e.Name()))
				g.AddPackage(&PackageNode{
					Name:     name,
					Path:     rel,
					Language: LangGeneric,
					Version:  "1.0.0",
				})
			}
		}
	}
}

func (g *DependencyGraph) scanGlobPattern(pattern string, onFound func(dir string)) {
	clean := strings.TrimSuffix(pattern, "/*")
	if strings.HasSuffix(pattern, "/*") {
		abs := filepath.Join(g.RootDir, clean)
		entries, err := os.ReadDir(abs)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
					onFound(filepath.Join(abs, e.Name()))
				}
			}
		}
	} else {
		abs := filepath.Join(g.RootDir, clean)
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			onFound(abs)
		}
	}
}
