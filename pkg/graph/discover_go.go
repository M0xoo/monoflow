package graph

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discoverGoModules registers every Go module (go.mod at any depth, including the repo
// root) and links them through require/replace directives.
func (g *DependencyGraph) discoverGoModules(files []string) {
	dirs := map[string]bool{}
	for _, d := range manifestDirs(files, "go.mod") {
		dirs[d] = true
	}
	for _, rel := range parseGoWorkUses(filepath.Join(g.RootDir, "go.work")) {
		if fileExistsAt(filepath.Join(g.RootDir, rel, "go.mod")) {
			dirs[filepath.ToSlash(filepath.Clean(rel))] = true
		}
	}
	testCmd := "go test ./..."
	if ciRunsGoTestsWithRace(g.RootDir) {
		testCmd = "go test -race ./..." // as CI does: a data race fails CI, so it must fail verify
	}
	var found []*PackageNode
	for rel := range dirs {
		name := rel
		if rel == "." {
			// The root module is named by its module path ("." is not a useful name).
			if info, ok := parseGoMod(filepath.Join(g.RootDir, "go.mod")); ok {
				name = info.Module
			}
		}
		found = append(found, &PackageNode{
			Name:         name,
			Path:         rel,
			Language:     LangGo,
			Version:      "1.0.0",
			TestCmd:      testCmd,
			LintCmd:      GoLintCmd,
			TypecheckCmd: "go build ./...",
			FixCmd:       "gofmt -w {files:.go}",
		})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	resolveGoDependencies(g.RootDir, found)
	for _, p := range found {
		g.AddPackage(p)
	}
}

// ciRunsGoTestsWithRace reports whether a GitHub Actions workflow runs go test with -race.
// Local CI without it passed a commit whose race GitHub's CI then failed.
func ciRunsGoTestsWithRace(root string) bool {
	files, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "go test") && strings.Contains(line, " -race") {
				return true
			}
		}
	}
	return false
}

// GoLintCmd fails on unformatted files (naming them) as well as vet findings, so agents
// never need raw gofmt/go vet to match what CI enforces. Hidden dirs are pruned: gofmt -l .
// would walk .agents/ (baseline checkouts, release snapshots) and report their files.
const GoLintCmd = `unformatted=$(find . -path '*/.*' -prune -o -name '*.go' -print | xargs gofmt -l); if [ -n "$unformatted" ]; then echo "gofmt needed (run gofmt -w):"; echo "$unformatted"; exit 1; fi; go vet ./...`

// goModInfo is the subset of a go.mod file needed to link in-repo modules.
type goModInfo struct {
	Module   string
	Requires []string
	Replaces map[string]string // module path -> local replacement dir (relative to the go.mod)
}

func parseGoMod(path string) (goModInfo, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return goModInfo{Replaces: map[string]string{}}, false
	}
	return parseGoModContent(string(data))
}

func parseGoModContent(content string) (goModInfo, bool) {
	info := goModInfo{Replaces: map[string]string{}}
	data := []byte(content)
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
