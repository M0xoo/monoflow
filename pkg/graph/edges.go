package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/antigravity/monoflow/pkg/semver"
)

// Edge modes between packages of the monorepo.
//
// A source edge builds the consumer against the dependency's live code (workspace:, Go
// replace/go.work, Cargo/uv path to the package dir): every change affects the consumer.
//
// A pinned edge builds the consumer against an internal release managed by MonoFlow: an
// immutable snapshot of the dependency at its release tag, materialised under
// .agents/releases/<slug>@<version>/ (see pkg/version releases). The consumer is not
// affected by later changes and upgrades at its own pace.
const (
	EdgeSource = "source"
	EdgePinned = "pinned"
)

// ReleasesDir is where internal release snapshots are materialised (git-ignored).
const ReleasesDir = ".agents/releases"

var releasePathRe = regexp.MustCompile(`\.agents/releases/([^/@]+)@([0-9][^/]*)`)

// ReleaseRef extracts the pinned version from a path into a release snapshot.
func ReleaseRef(path string) (slug, version string, ok bool) {
	m := releasePathRe.FindStringSubmatch(filepath.ToSlash(path))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ReleaseSlug is the directory-safe name of a package used in release snapshot paths.
func ReleaseSlug(pkgName string) string {
	r := strings.NewReplacer("/", "-", "@", "", ":", "-")
	return strings.Trim(r.Replace(pkgName), "-")
}

// pinnedOrSource sets the mode of an edge whose manifest points at a local path.
func pinnedOrSource(e *Edge, path string) {
	if _, ver, ok := ReleaseRef(path); ok {
		e.Mode = EdgePinned
		e.Constraint = ver
		e.Satisfied = true
		return
	}
	e.Mode = EdgeSource
}

// Edge is one in-repo dependency with how it is consumed.
type Edge struct {
	Consumer          string `json:"consumer"`
	Dependency        string `json:"dependency"`
	Ecosystem         string `json:"ecosystem"`
	Mode              string `json:"mode"`
	Constraint        string `json:"constraint,omitempty"`
	DependencyVersion string `json:"dependency_version,omitempty"`
	Satisfied         bool   `json:"satisfied"`
	Problem           string `json:"problem,omitempty"`
}

// ScanEdges reads every package manifest and returns its in-repo dependency edges.
func (g *DependencyGraph) ScanEdges() []Edge {
	pkgs := g.GetAllPackages()
	byName := map[string]*PackageNode{}
	byPy := map[string]*PackageNode{}
	byGoModule := map[string]*PackageNode{}
	for _, p := range pkgs {
		byName[p.Name] = p
		if p.Language == LangPython {
			byPy[normalizePyName(p.Name)] = p
		}
		if p.Language == LangGo {
			if info, ok := parseGoModContent(manifest(g.RootDir, filepath.Join(p.Path, "go.mod"))); ok {
				byGoModule[info.Module] = p
			}
		}
	}
	goWork := map[string]bool{}
	for _, u := range parseGoWorkUses(filepath.Join(g.RootDir, "go.work")) {
		goWork[filepath.ToSlash(filepath.Clean(u))] = true
	}
	cargoWorkspaceDeps := tomlSections(readFileString(filepath.Join(g.RootDir, "Cargo.toml")))["workspace.dependencies"]

	var edges []Edge
	for _, p := range pkgs {
		read := func(name string) string { return manifest(g.RootDir, filepath.Join(p.Path, name)) }
		switch p.Language {
		case LangJavaScript, LangTypeScript:
			edges = append(edges, npmEdges(p, read("package.json"), byName)...)
		case LangGo:
			edges = append(edges, goEdges(p, read("go.mod"), byGoModule, goWork)...)
		case LangRust:
			edges = append(edges, cargoEdges(p, read("Cargo.toml"), byName, cargoWorkspaceDeps)...)
		case LangPython:
			edges = append(edges, pythonEdges(p, read("pyproject.toml"), byPy)...)
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Consumer != edges[j].Consumer {
			return edges[i].Consumer < edges[j].Consumer
		}
		return edges[i].Dependency < edges[j].Dependency
	})
	return edges
}

// ApplyEdgeModes keeps only source edges as Dependencies (they propagate "affected");
// pinned edges move to PinnedDependencies.
func (g *DependencyGraph) ApplyEdgeModes() {
	modes := map[string]map[string]string{}
	for _, e := range g.ScanEdges() {
		if modes[e.Consumer] == nil {
			modes[e.Consumer] = map[string]string{}
		}
		modes[e.Consumer][e.Dependency] = e.Mode
	}
	g.mu.Lock()
	for name, p := range g.packages {
		m := modes[name]
		if m == nil {
			continue
		}
		var src, pinned []string
		for _, d := range p.Dependencies {
			if m[d] == EdgePinned {
				pinned = append(pinned, d)
			} else {
				src = append(src, d)
			}
		}
		for d, mode := range m {
			if mode == EdgePinned && !contains(pinned, d) {
				pinned = append(pinned, d)
			}
		}
		if src == nil {
			src = []string{}
		}
		sort.Strings(pinned)
		p.Dependencies = src
		p.PinnedDependencies = pinned
	}
	g.mu.Unlock()
	g.RebuildEdges()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// manifest returns a package manifest from the working tree, or from HEAD when a sparse
// worktree does not project that package (its edges must stay visible).
func manifest(root, rel string) string {
	if data, err := os.ReadFile(filepath.Join(root, rel)); err == nil {
		return string(data)
	}
	out, err := exec.Command("git", "-C", root, "show", "HEAD:"+filepath.ToSlash(rel)).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func check(eco semver.Ecosystem, e *Edge) {
	if e.Constraint == "" || e.DependencyVersion == "" {
		e.Satisfied = true
		return
	}
	ok, err := semver.Satisfies(eco, e.Constraint, e.DependencyVersion)
	if err != nil {
		e.Satisfied = true // unknown dialect: do not guess
		return
	}
	e.Satisfied = ok
}

func npmEdges(p *PackageNode, content string, byName map[string]*PackageNode) []Edge {
	var pj npmPackageJSON
	if json.Unmarshal([]byte(content), &pj) != nil {
		return nil
	}
	var out []Edge
	seen := map[string]bool{}
	for _, m := range []map[string]string{pj.Dependencies, pj.DevDependencies, pj.PeerDependencies} {
		for name, spec := range m {
			dep, ok := byName[name]
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			e := Edge{Consumer: p.Name, Dependency: name, Ecosystem: "npm", Constraint: spec, DependencyVersion: dep.Version}
			local := strings.HasPrefix(spec, "workspace:") || strings.HasPrefix(spec, "file:") || strings.HasPrefix(spec, "link:") || strings.HasPrefix(spec, "portal:")
			if strings.HasPrefix(spec, "file:") || strings.HasPrefix(spec, "link:") || strings.HasPrefix(spec, "portal:") {
				e.Constraint = ""
			}
			check(semver.EcoNPM, &e)
			if _, _, rel := ReleaseRef(spec); rel {
				pinnedOrSource(&e, spec)
				out = append(out, e)
				continue
			}
			switch {
			case local:
				e.Mode = EdgeSource
				if !e.Satisfied {
					e.Problem = fmt.Sprintf("%s requires %s but the workspace has %s: install/link fails", spec, name, dep.Version)
				}
			case e.Satisfied:
				e.Mode = EdgeSource // npm/yarn/bun link the local package when it satisfies the range
			default:
				e.Mode = EdgePinned // resolved from the registry (released version)
			}
			out = append(out, e)
		}
	}
	return out
}

func goEdges(p *PackageNode, content string, byModule map[string]*PackageNode, goWork map[string]bool) []Edge {
	info, ok := parseGoModContent(content)
	if !ok {
		return nil
	}
	versions := goRequireVersions(content)
	var out []Edge
	for _, req := range info.Requires {
		dep, ok := byModule[req]
		if !ok || dep.Name == p.Name {
			continue
		}
		e := Edge{Consumer: p.Name, Dependency: dep.Name, Ecosystem: "go", Constraint: versions[req], DependencyVersion: dep.Version, Satisfied: true}
		target, replaced := info.Replaces[req]
		switch {
		case replaced:
			pinnedOrSource(&e, target) // replace to the live module or to a release snapshot
		case goWork[filepath.ToSlash(p.Path)] && goWork[filepath.ToSlash(dep.Path)]:
			e.Mode = EdgeSource
		default:
			e.Mode = EdgePinned // resolved as a published module version
		}
		out = append(out, e)
	}
	return out
}

var goRequireRe = regexp.MustCompile(`^\s*(?:require\s+)?([^\s()]+)\s+(v[0-9][^\s]*)`)

func goRequireVersions(content string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "replace") || strings.Contains(l, "=>") {
			continue
		}
		if m := goRequireRe.FindStringSubmatch(l); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

var (
	tomlInlineVal = regexp.MustCompile(`^["']?([A-Za-z0-9_.-]+?)["']?\s*=\s*(.*)$`)
	tomlStrVal    = regexp.MustCompile(`^["']([^"']*)["']`)
	tomlField     = func(key string) *regexp.Regexp {
		return regexp.MustCompile(`\b` + key + `\s*=\s*(?:["']([^"']*)["']|(true|false))`)
	}
	fieldVersion   = tomlField("version")
	fieldPath      = tomlField("path")
	fieldPackage   = tomlField("package")
	fieldWorkspace = tomlField("workspace")
)

func cargoEdges(p *PackageNode, content string, byName map[string]*PackageNode, wsDeps []string) []Edge {
	sections := tomlSections(content)
	type spec struct{ version, path string }
	specs := map[string]spec{}
	parse := func(name, val string) {
		s := spec{}
		if m := tomlStrVal.FindStringSubmatch(val); m != nil {
			s.version = m[1]
		}
		if m := fieldVersion.FindStringSubmatch(val); m != nil {
			s.version = m[1]
		}
		if m := fieldPath.FindStringSubmatch(val); m != nil {
			s.path = m[1]
		}
		if m := fieldPackage.FindStringSubmatch(val); m != nil {
			name = m[1]
		}
		if m := fieldWorkspace.FindStringSubmatch(val); m != nil && m[2] == "true" {
			for _, l := range wsDeps {
				if wm := tomlInlineVal.FindStringSubmatch(l); wm != nil && wm[1] == name {
					ws := wm[2]
					if vm := tomlStrVal.FindStringSubmatch(ws); vm != nil {
						s.version = vm[1]
					}
					if vm := fieldVersion.FindStringSubmatch(ws); vm != nil {
						s.version = vm[1]
					}
					if vm := fieldPath.FindStringSubmatch(ws); vm != nil {
						s.path = vm[1]
					}
				}
			}
		}
		if _, ok := byName[name]; ok {
			specs[name] = s
		}
	}
	for sec, lines := range sections {
		last := sec[strings.LastIndex(sec, ".")+1:]
		switch {
		case last == "dependencies" || last == "dev-dependencies" || last == "build-dependencies":
			for _, l := range lines {
				if m := tomlInlineVal.FindStringSubmatch(l); m != nil {
					key := strings.TrimSuffix(m[1], ".workspace")
					val := m[2]
					if strings.HasSuffix(m[1], ".workspace") {
						val = "workspace = " + val
					}
					parse(key, val)
				}
			}
		case strings.Contains(sec, "dependencies."):
			parse(last, strings.Join(lines, " "))
		}
	}
	var out []Edge
	for name, s := range specs {
		dep := byName[name]
		if dep.Name == p.Name {
			continue
		}
		e := Edge{Consumer: p.Name, Dependency: name, Ecosystem: "cargo", Constraint: s.version, DependencyVersion: dep.Version}
		check(semver.EcoCargo, &e)
		if _, _, rel := ReleaseRef(s.path); rel {
			pinnedOrSource(&e, s.path)
		} else if s.path != "" {
			e.Mode = EdgeSource
			if !e.Satisfied {
				e.Problem = fmt.Sprintf("path dependency requires %s %q but the crate is %s: cargo refuses to build", name, s.version, dep.Version)
			}
		} else {
			e.Mode = EdgePinned
		}
		out = append(out, e)
	}
	return out
}

var pyReq = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*([^;]*)`)

func pythonEdges(p *PackageNode, content string, byPy map[string]*PackageNode) []Edge {
	sections := tomlSections(content)
	reqs := tomlArray(sections["project"], "dependencies")
	for sec, lines := range sections {
		if sec == "project.optional-dependencies" || sec == "dependency-groups" {
			for _, k := range tomlKeys(lines) {
				reqs = append(reqs, tomlArray(lines, k)...)
			}
		}
	}
	localSource := map[string]bool{}
	releasePath := map[string]string{}
	for _, l := range sections["tool.uv.sources"] {
		if m := tomlInlineVal.FindStringSubmatch(l); m != nil && (fieldPath.MatchString(m[2]) || strings.Contains(m[2], "workspace")) {
			localSource[normalizePyName(m[1])] = true
			if pm := fieldPath.FindStringSubmatch(m[2]); pm != nil {
				releasePath[normalizePyName(m[1])] = pm[1]
			}
		}
	}
	for sec, lines := range sections {
		if sec == "tool.poetry.dependencies" || strings.HasPrefix(sec, "tool.poetry.group.") {
			for _, l := range lines {
				if m := tomlInlineVal.FindStringSubmatch(l); m != nil {
					if fieldPath.MatchString(m[2]) {
						localSource[normalizePyName(m[1])] = true
					} else if v := tomlStrVal.FindStringSubmatch(m[2]); v != nil {
						reqs = append(reqs, m[1]+poetryToPEP440(v[1]))
					}
				}
			}
		}
	}
	var out []Edge
	seen := map[string]bool{}
	for _, r := range reqs {
		m := pyReq.FindStringSubmatch(r)
		if m == nil {
			continue
		}
		dep, ok := byPy[normalizePyName(m[1])]
		if !ok || dep.Name == p.Name || seen[dep.Name] {
			continue
		}
		seen[dep.Name] = true
		e := Edge{Consumer: p.Name, Dependency: dep.Name, Ecosystem: "python", Constraint: strings.TrimSpace(m[3]), DependencyVersion: dep.Version}
		check(semver.EcoPython, &e)
		if rp := releasePath[normalizePyName(m[1])]; rp != "" {
			if _, _, rel := ReleaseRef(rp); rel {
				pinnedOrSource(&e, rp)
				out = append(out, e)
				continue
			}
		}
		if localSource[normalizePyName(m[1])] {
			e.Mode = EdgeSource
			if !e.Satisfied {
				e.Problem = fmt.Sprintf("local source requires %s %s but the project is %s: resolution fails", dep.Name, e.Constraint, dep.Version)
			}
		} else {
			e.Mode = EdgePinned
		}
		out = append(out, e)
	}
	for name := range localSource {
		if dep, ok := byPy[name]; ok && !seen[dep.Name] && dep.Name != p.Name {
			out = append(out, Edge{Consumer: p.Name, Dependency: dep.Name, Ecosystem: "python", Mode: EdgeSource, DependencyVersion: dep.Version, Satisfied: true})
		}
	}
	return out
}

// poetryToPEP440 converts Poetry's caret/tilde shorthand into a PEP 440 specifier.
func poetryToPEP440(v string) string {
	v = strings.TrimSpace(v)
	if v == "*" || v == "" {
		return ""
	}
	if strings.HasPrefix(v, "^") || strings.HasPrefix(v, "~") {
		return " " + v // handled by the shared caret/tilde rules
	}
	return " " + v
}
