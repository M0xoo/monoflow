package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Language represents the programming language ecosystem of a package.
type Language string

const (
	LangTypeScript Language = "typescript"
	LangJavaScript Language = "javascript"
	LangGo         Language = "go"
	LangRust       Language = "rust"
	LangPython     Language = "python"
	LangProto      Language = "proto"
	LangGeneric    Language = "generic"
)

// PackageNode represents a hermetic package within the monorepo.
type PackageNode struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"` // Relative to root
	Language     Language `json:"language"`
	Version      string   `json:"version,omitempty"`
	Dependencies []string `json:"dependencies"` // Upstream source dependencies (live code)
	// PinnedDependencies are in-repo packages consumed at an internal release; changes to
	// them do not affect this package until it upgrades.
	PinnedDependencies []string `json:"pinned_dependencies,omitempty"`
	TestCmd            string   `json:"test_cmd,omitempty"`
	LintCmd            string   `json:"lint_cmd,omitempty"`
	TypecheckCmd       string   `json:"typecheck_cmd,omitempty"`
	Visibility         string   `json:"visibility,omitempty"` // "public" or "internal"
}

// Config represents .agents/monoflow.config.json
type Config struct {
	Version      string        `json:"version"`
	MonorepoType string        `json:"monorepo_type,omitempty"` // turbo, nx, pnpm, cargo, go, custom
	Packages     []PackageNode `json:"packages"`
}

// DependencyGraph manages package relations and computes affected targets.
type DependencyGraph struct {
	mu            sync.RWMutex
	RootDir       string
	packages      map[string]*PackageNode
	upstreamMap   map[string]map[string]bool // pkg -> set of dependencies
	downstreamMap map[string]map[string]bool // pkg -> set of dependents
}

// New creates an empty DependencyGraph.
func New(rootDir string) *DependencyGraph {
	return &DependencyGraph{
		RootDir:       rootDir,
		packages:      make(map[string]*PackageNode),
		upstreamMap:   make(map[string]map[string]bool),
		downstreamMap: make(map[string]map[string]bool),
	}
}

// LoadOrDiscover initializes the graph from config or auto-detects from disk.
func LoadOrDiscover(rootDir string) (*DependencyGraph, error) {
	g := New(rootDir)
	configPath := filepath.Join(rootDir, ".agents", "monoflow.config.json")

	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var cfg Config
			if err := json.Unmarshal(data, &cfg); err == nil && len(cfg.Packages) > 0 {
				var unresolvedGo []*PackageNode
				for _, p := range cfg.Packages {
					pCopy := p
					// Configs written before Go dependency discovery store null deps for
					// Go modules; derive them from go.mod rather than trusting an empty graph.
					if pCopy.Language == LangGo && pCopy.Dependencies == nil {
						unresolvedGo = append(unresolvedGo, &pCopy)
					}
					g.AddPackage(&pCopy)
				}
				if len(unresolvedGo) > 0 {
					var goPkgs []*PackageNode
					for _, p := range g.packages {
						if p.Language == LangGo {
							goPkgs = append(goPkgs, p)
						}
					}
					resolveGoDependencies(rootDir, goPkgs, unresolvedGo...)
				}
				g.rebuildEdges()
				// Manifests are the source of truth for how each edge is consumed.
				g.ApplyEdgeModes()
				return g, nil
			}
		}
	}

	// Auto-discovery fallback
	if err := g.AutoDiscover(); err != nil {
		return nil, err
	}
	return g, nil
}

// SaveConfig serializes the graph into .agents/monoflow.config.json format.
func (g *DependencyGraph) SaveConfig(configPath string) error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}

	pkgs := make([]PackageNode, 0, len(g.packages))
	for _, p := range g.packages {
		pkgs = append(pkgs, *p)
	}

	cfg := Config{
		Version:  "1.0",
		Packages: pkgs,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

// AddPackage adds a package to the graph.
func (g *DependencyGraph) AddPackage(pkg *PackageNode) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Normalize relative path
	cleanPath := filepath.Clean(pkg.Path)
	cleanPath = strings.TrimPrefix(cleanPath, string(filepath.Separator))
	pkg.Path = cleanPath

	g.packages[pkg.Name] = pkg
	if _, ok := g.upstreamMap[pkg.Name]; !ok {
		g.upstreamMap[pkg.Name] = make(map[string]bool)
	}
	if _, ok := g.downstreamMap[pkg.Name]; !ok {
		g.downstreamMap[pkg.Name] = make(map[string]bool)
	}
}

// RebuildEdges populates upstream and downstream maps and handles cycles safely.
func (g *DependencyGraph) RebuildEdges() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.upstreamMap = make(map[string]map[string]bool)
	g.downstreamMap = make(map[string]map[string]bool)

	for name := range g.packages {
		g.upstreamMap[name] = make(map[string]bool)
		g.downstreamMap[name] = make(map[string]bool)
	}

	for name, pkg := range g.packages {
		for _, dep := range pkg.Dependencies {
			// Only link if the dependency is also a package within this monorepo
			if _, exists := g.packages[dep]; exists {
				g.upstreamMap[name][dep] = true
				g.downstreamMap[dep][name] = true
			}
		}
	}
}

func (g *DependencyGraph) rebuildEdges() {
	g.RebuildEdges()
}

// GetPackage retrieves a package by name, falling back to its directory path
// (e.g. "packages/ui" for "@acme/ui") so agents can address packages either way.
func (g *DependencyGraph) GetPackage(name string) (*PackageNode, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if p, ok := g.packages[name]; ok {
		return p, true
	}
	clean := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(strings.TrimPrefix(name, "./"))), "/")
	for _, p := range g.packages {
		if filepath.ToSlash(p.Path) == clean {
			return p, true
		}
	}
	// Last resort: a unique short name ("money" for libs/money, "ui" for @acme/ui).
	var match *PackageNode
	for _, p := range g.packages {
		short := p.Name[strings.LastIndexAny(p.Name, "/")+1:]
		if short == clean || filepath.Base(p.Path) == clean {
			if match != nil && match != p {
				return nil, false
			}
			match = p
		}
	}
	return match, match != nil
}

// GetAllPackages returns a sorted list of all packages.
func (g *DependencyGraph) GetAllPackages() []*PackageNode {
	g.mu.RLock()
	defer g.mu.RUnlock()

	list := make([]*PackageNode, 0, len(g.packages))
	for _, p := range g.packages {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}

// GetUpstream returns direct upstream dependencies for a package.
func (g *DependencyGraph) GetUpstream(name string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	deps, ok := g.upstreamMap[name]
	if !ok {
		return nil
	}
	res := make([]string, 0, len(deps))
	for d := range deps {
		res = append(res, d)
	}
	sort.Strings(res)
	return res
}

// GetDownstream returns direct downstream dependents for a package.
func (g *DependencyGraph) GetDownstream(name string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	deps, ok := g.downstreamMap[name]
	if !ok {
		return nil
	}
	res := make([]string, 0, len(deps))
	for d := range deps {
		res = append(res, d)
	}
	sort.Strings(res)
	return res
}

// GetTransitiveDownstream returns all direct and transitive dependents with cycle prevention.
func (g *DependencyGraph) GetTransitiveDownstream(name string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()

	visited := make(map[string]bool)
	g.traverseDownstream(name, visited)

	// Remove self
	delete(visited, name)

	res := make([]string, 0, len(visited))
	for v := range visited {
		res = append(res, v)
	}
	sort.Strings(res)
	return res
}

func (g *DependencyGraph) traverseDownstream(current string, visited map[string]bool) {
	if visited[current] {
		return // Cycle detected or already visited
	}
	visited[current] = true

	for dep := range g.downstreamMap[current] {
		g.traverseDownstream(dep, visited)
	}
}

// MapFileToPackage resolves which package owns a specific relative file path.
// Uses longest prefix match to prevent nested directory collisions.
func (g *DependencyGraph) MapFileToPackage(relFilePath string) *PackageNode {
	g.mu.RLock()
	defer g.mu.RUnlock()

	clean := filepath.Clean(relFilePath)
	clean = strings.ReplaceAll(clean, "\\", "/")
	clean = strings.TrimPrefix(clean, "/")

	var bestMatch *PackageNode
	longestPrefix := -1

	for _, pkg := range g.packages {
		pkgPath := strings.ReplaceAll(pkg.Path, "\\", "/")
		pkgPath = strings.TrimPrefix(pkgPath, "/")
		if pkgPath == "." || pkgPath == "" {
			if longestPrefix < 0 {
				longestPrefix = 0
				bestMatch = pkg
			}
			continue
		}
		if clean == pkgPath || strings.HasPrefix(clean, pkgPath+"/") {
			if len(pkgPath) > longestPrefix {
				longestPrefix = len(pkgPath)
				bestMatch = pkg
			}
		}
	}

	return bestMatch
}

// AffectedResult contains the exact calculation of affected targets.
type AffectedResult struct {
	ChangedFiles       []string `json:"changed_files"`
	DirectlyChanged    []string `json:"directly_changed"`
	TransitiveAffected []string `json:"transitive_affected"`
	AllAffected        []string `json:"all_affected"`
}

// ComputeAffected computes direct and transitive affected packages from a list of modified files.
func (g *DependencyGraph) ComputeAffected(changedFiles []string) AffectedResult {
	directMap := make(map[string]bool)
	for _, f := range changedFiles {
		if IsNonCodeFile(f) {
			continue
		}
		if IsGlobalFile(f) {
			// Workspace-level config (go.work, lockfiles, CI) can break every package,
			// even when a root package technically owns the file; remote CI re-runs
			// everything, so must we.
			for name := range g.packages {
				directMap[name] = true
			}
		} else if pkg := g.MapFileToPackage(f); pkg != nil {
			directMap[pkg.Name] = true
		}
	}

	allMap := make(map[string]bool)
	transitiveMap := make(map[string]bool)

	for direct := range directMap {
		allMap[direct] = true
		downstream := g.GetTransitiveDownstream(direct)
		for _, down := range downstream {
			allMap[down] = true
			if !directMap[down] {
				transitiveMap[down] = true
			}
		}
	}

	directList := toSortedList(directMap)
	transitiveList := toSortedList(transitiveMap)
	allList := toSortedList(allMap)

	return AffectedResult{
		ChangedFiles:       changedFiles,
		DirectlyChanged:    directList,
		TransitiveAffected: transitiveList,
		AllAffected:        allList,
	}
}

// SparseCheckoutPaths computes the minimal paths needed to build/test a target package
// (the package itself + all upstream transitive dependencies).
func (g *DependencyGraph) SparseCheckoutPaths(targetName string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	target, exists := g.packages[targetName]
	if !exists {
		return nil, fmt.Errorf("package '%s' not found in graph", targetName)
	}

	visited := make(map[string]bool)
	g.traverseUpstream(targetName, visited)

	paths := make([]string, 0, len(visited))
	paths = append(paths, target.Path)

	for pName := range visited {
		if p, ok := g.packages[pName]; ok {
			paths = append(paths, p.Path)
		}
	}

	sort.Strings(paths)
	return paths, nil
}

func (g *DependencyGraph) traverseUpstream(current string, visited map[string]bool) {
	if visited[current] {
		return
	}
	visited[current] = true

	for dep := range g.upstreamMap[current] {
		g.traverseUpstream(dep, visited)
	}
}

func toSortedList(m map[string]bool) []string {
	res := make([]string, 0, len(m))
	for k := range m {
		res = append(res, k)
	}
	sort.Strings(res)
	return res
}

var globalFilePattern = regexp.MustCompile(`^(go\.work(\.sum)?|package\.json|package-lock\.json|npm-shrinkwrap\.json|pnpm-lock\.yaml|pnpm-workspace\.yaml|yarn\.lock|bun\.lockb?|tsconfig[^/]*\.json|turbo\.json|nx\.json|Cargo\.(toml|lock)|Makefile|\.tool-versions|\.nvmrc|\.go-version|ci/.+|\.github/workflows/.+|\.agents/monoflow\.config\.json)$`)

// IsGlobalFile reports whether a repo-relative path is workspace-wide configuration
// whose change can affect every package.
func IsGlobalFile(path string) bool {
	return globalFilePattern.MatchString(strings.TrimPrefix(filepath.ToSlash(path), "./"))
}

var nonCodePattern = regexp.MustCompile(`(?i)(\.(md|mdx|rst)$|^LICENSE|(^|/)docs?/|^\.agents/|^\.claude/|^\.opencode/|^\.cursor/)`)

// IsNonCodeFile reports files whose change cannot break a build or test (docs, agent
// config). Kept in sync with the paths-ignore of the generated CI workflow.
func IsNonCodeFile(path string) bool {
	return nonCodePattern.MatchString(strings.TrimPrefix(filepath.ToSlash(path), "./"))
}
