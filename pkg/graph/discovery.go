package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
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

					rel, _ := filepath.Rel(g.RootDir, dir)
					g.AddPackage(&PackageNode{
						Name:         name,
						Path:         rel,
						Language:     LangTypeScript,
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
	// Look for go.work or multiple go.mod files
	entries, err := os.ReadDir(g.RootDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		// Search 1 level down
		subEntries, _ := os.ReadDir(filepath.Join(g.RootDir, entry.Name()))
		for _, sub := range subEntries {
			if sub.IsDir() {
				targetDir := filepath.Join(g.RootDir, entry.Name(), sub.Name())
				goMod := filepath.Join(targetDir, "go.mod")
				if _, err := os.Stat(goMod); err == nil {
					name := entry.Name() + "/" + sub.Name()
					rel, _ := filepath.Rel(g.RootDir, targetDir)
					g.AddPackage(&PackageNode{
						Name:         name,
						Path:         rel,
						Language:     LangGo,
						TestCmd:      "go test -v ./...",
						LintCmd:      "go vet ./...",
						TypecheckCmd: "go build ./...",
					})
				}
			}
		}
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
