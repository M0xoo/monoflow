package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// AutoDiscover finds every package in the workspace from its manifests (package.json,
// go.mod, Cargo.toml, pyproject.toml/setup.py) at any depth, and links in-repo
// dependencies from those manifests so `graph`, `verify --affected` and sparse worktrees
// see the real dependency graph of an existing monorepo.
func (g *DependencyGraph) AutoDiscover() error {
	files := listWorkspaceFiles(g.RootDir)

	g.discoverNodeWorkspaces(files)
	g.discoverCargoPackages(files)
	g.discoverGoModules(files)
	g.discoverPythonPackages(files)
	g.discoverContracts()

	if len(g.packages) == 0 {
		g.discoverByDirectoryHeuristic()
	}

	g.rebuildEdges()
	return nil
}

// skippedDirs never contain workspace packages (vendored code, fixtures, build output).
var skippedDirs = map[string]bool{
	"node_modules": true, "vendor": true, "testdata": true, "__fixtures__": true, "fixtures": true,
	".agents": true, ".git": true, "target": true, "dist": true, "build": true, ".venv": true,
	"venv": true, "__pycache__": true, "third_party": true,
}

// listWorkspaceFiles returns repo-relative, slash-separated paths of tracked and
// untracked-but-not-ignored files (git), falling back to a filesystem walk.
func listWorkspaceFiles(root string) []string {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, f := range strings.Split(string(out), "\n") {
			if f != "" && !inSkippedDir(f) {
				files = append(files, f)
			}
		}
		return files
	}
	var files []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && (skippedDirs[info.Name()] || (strings.HasPrefix(info.Name(), ".") && p != root)) {
			return filepath.SkipDir
		}
		if !info.IsDir() {
			if rel, err := filepath.Rel(root, p); err == nil {
				files = append(files, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return files
}

func inSkippedDir(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		if skippedDirs[p] {
			return true
		}
	}
	return false
}

// manifestDirs returns the directories (repo-relative, "." for the root) containing a
// file with the given base name.
func manifestDirs(files []string, base string) []string {
	var dirs []string
	for _, f := range files {
		if f == base || strings.HasSuffix(f, "/"+base) {
			dirs = append(dirs, filepath.ToSlash(filepath.Dir(f)))
		}
	}
	return dirs
}

// globToRegexp converts a workspace glob ("packages/*", "apps/**", "libs/*/sub") into an
// anchored regexp over slash-separated directory paths.
func globToRegexp(glob string) *regexp.Regexp {
	glob = strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(glob), "./"), "/")
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// tomlSections splits a TOML document into "[section]" -> lines (top-level keys under "").
// Only what manifest dependency extraction needs: headers, keys and multi-line arrays.
func tomlSections(content string) map[string][]string {
	sections := map[string][]string{}
	cur := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && !strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]") && !strings.Contains(line, "=") {
			cur = strings.TrimSpace(strings.Trim(line, "[]"))
			continue
		}
		sections[cur] = append(sections[cur], line)
	}
	return sections
}

// tomlString returns the string value of key in section lines.
func tomlString(lines []string, key string) string {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `\s*=\s*["']([^"']*)["']`)
	for _, l := range lines {
		if m := re.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	return ""
}

// tomlKeys returns the keys defined in section lines ("foo = ...", "foo.workspace = true").
func tomlKeys(lines []string) []string {
	re := regexp.MustCompile(`^["']?([A-Za-z0-9_.-]+?)["']?(\.[A-Za-z]+)?\s*=`)
	var keys []string
	depth := 0
	for _, l := range lines {
		if depth == 0 {
			if m := re.FindStringSubmatch(l); m != nil {
				keys = append(keys, m[1])
			}
		}
		depth += strings.Count(l, "[") - strings.Count(l, "]")
		if depth < 0 {
			depth = 0
		}
	}
	return keys
}

// tomlArray returns the quoted strings of a (possibly multi-line) array value of key.
func tomlArray(lines []string, key string) []string {
	var out []string
	in := false
	quoted := regexp.MustCompile(`["']([^"']+)["']`)
	start := regexp.MustCompile(`^["']?` + regexp.QuoteMeta(key) + `["']?\s*=\s*\[`)
	for _, l := range lines {
		if !in {
			if !start.MatchString(l) {
				continue
			}
			in = true
			l = l[strings.Index(l, "[")+1:]
		}
		end := strings.Contains(l, "]")
		if end {
			l = l[:strings.LastIndex(l, "]")]
		}
		for _, m := range quoted.FindAllStringSubmatch(l, -1) {
			out = append(out, m[1])
		}
		if end {
			in = false
		}
	}
	return out
}

func readFileString(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
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
