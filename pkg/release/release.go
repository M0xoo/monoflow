// Package release implements MonoFlow's internal package releases: an immutable per-package
// git tag "<slug>@<version>" plus an on-demand snapshot of the package (and the live
// dependencies it was built with) under .agents/releases/<slug>@<version>/, keeping the
// repo-relative layout so relative paths inside the snapshot still resolve.
//
// Consumers pin a release by pointing their manifest at the snapshot path; they are then
// isolated from later changes to the package and upgrade at their own pace.
package release

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/semver"
)

// Tag returns the git tag of a package release.
func Tag(pkgName, version string) string {
	return graph.ReleaseSlug(pkgName) + "@" + version
}

// SnapshotDir is the repo-relative snapshot root of a release.
func SnapshotDir(pkgName, version string) string {
	return filepath.ToSlash(filepath.Join(graph.ReleasesDir, Tag(pkgName, version)))
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// List returns the released versions of a package, ascending.
func List(root, pkgName string) []string {
	out, err := git(root, "tag", "--list", graph.ReleaseSlug(pkgName)+"@*")
	if err != nil || out == "" {
		return nil
	}
	var vers []string
	for _, t := range strings.Fields(out) {
		v := t[strings.LastIndex(t, "@")+1:]
		if _, err := semver.Parse(v); err == nil {
			vers = append(vers, v)
		}
	}
	sort.Slice(vers, func(i, j int) bool { return less(vers[i], vers[j]) })
	return vers
}

func less(a, b string) bool {
	x, _ := semver.Parse(a)
	y, _ := semver.Parse(b)
	if x.Major != y.Major {
		return x.Major < y.Major
	}
	if x.Minor != y.Minor {
		return x.Minor < y.Minor
	}
	return x.Patch < y.Patch
}

// Latest returns the highest released version of a package ("" if none).
func Latest(root, pkgName string) string {
	v := List(root, pkgName)
	if len(v) == 0 {
		return ""
	}
	return v[len(v)-1]
}

// Create tags HEAD as a release of pkg at its current manifest version. The package's
// files must be committed, so the tag captures exactly what consumers will build.
func Create(root string, g *graph.DependencyGraph, pkgName string) (string, error) {
	pkg, ok := g.GetPackage(pkgName)
	if !ok {
		return "", fmt.Errorf("package %q not found", pkgName)
	}
	if out, _ := git(root, "status", "--porcelain", "--", pkg.Path); out != "" {
		return "", fmt.Errorf("package %s has uncommitted changes; commit them before releasing", pkg.Name)
	}
	tag := Tag(pkg.Name, pkg.Version)
	if _, err := git(root, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag); err == nil {
		return "", fmt.Errorf("release %s already exists; bump the version first (monoflow version bump %s <patch|minor|major>)", tag, pkg.Name)
	}
	if _, err := git(root, "tag", "-a", tag, "-m", "MonoFlow release "+tag); err != nil {
		return "", err
	}
	return tag, nil
}

// Materialize extracts a release snapshot (package + transitive live dependencies, as they
// were at the tag) into .agents/releases, once. Returns the absolute snapshot root.
func Materialize(root string, g *graph.DependencyGraph, pkgName, version string) (string, error) {
	pkg, ok := g.GetPackage(pkgName)
	if !ok {
		return "", fmt.Errorf("package %q not found", pkgName)
	}
	dest := filepath.Join(root, SnapshotDir(pkg.Name, version))
	if _, err := os.Stat(filepath.Join(dest, ".monoflow-release")); err == nil {
		return dest, nil
	}
	tag := Tag(pkg.Name, version)
	if _, err := git(root, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag); err != nil {
		_, _ = git(root, "fetch", "--quiet", "origin", "refs/tags/"+tag+":refs/tags/"+tag)
		if _, err := git(root, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag); err != nil {
			return "", fmt.Errorf("release %s not found (no tag)", tag)
		}
	}
	paths := []string{pkg.Path}
	seen := map[string]bool{pkg.Name: true}
	queue := append([]string{}, pkg.Dependencies...)
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if seen[d] {
			continue
		}
		seen[d] = true
		if dp, ok := g.GetPackage(d); ok {
			paths = append(paths, dp.Path)
			queue = append(queue, dp.Dependencies...)
		}
	}
	// Workspace-level files the package may need (go.work is deliberately excluded:
	// a snapshot builds standalone with its own replace directives).
	for _, f := range []string{"package.json", "tsconfig.json", "tsconfig.base.json", "Cargo.toml"} {
		if _, err := git(root, "cat-file", "-e", tag+":"+f); err == nil {
			paths = append(paths, f)
		}
	}
	tmp := dest + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0755); err != nil {
		return "", err
	}
	archive := exec.Command("git", append([]string{"archive", "--format=tar", tag, "--"}, paths...)...)
	archive.Dir = root
	var tarball bytes.Buffer
	archive.Stdout = &tarball
	if err := archive.Run(); err != nil {
		return "", fmt.Errorf("git archive %s: %w", tag, err)
	}
	untar := exec.Command("tar", "-x", "-C", tmp)
	untar.Stdin = &tarball
	if out, err := untar.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extract %s: %v (%s)", tag, err, out)
	}
	_ = os.WriteFile(filepath.Join(tmp, ".monoflow-release"), []byte(tag+"\n"), 0644)
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// EnsurePinned materialises every release snapshot referenced by a pinned edge.
func EnsurePinned(root string, g *graph.DependencyGraph) []error {
	var errs []error
	done := map[string]bool{}
	for _, e := range g.ScanEdges() {
		if e.Mode != graph.EdgePinned || e.Constraint == "" || done[e.Dependency+"@"+e.Constraint] {
			continue
		}
		done[e.Dependency+"@"+e.Constraint] = true
		if _, err := semver.Parse(e.Constraint); err != nil {
			continue // pinned to an external/published version, not an internal snapshot
		}
		if _, err := Materialize(root, g, e.Dependency, e.Constraint); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Repoint rewrites consumer's manifest so its dependency dep points at target: either a
// release snapshot (pin/upgrade, version != "") or the live package (unpin, version == "").
func Repoint(root string, g *graph.DependencyGraph, consumer, dep, version string) (string, error) {
	c, ok := g.GetPackage(consumer)
	if !ok {
		return "", fmt.Errorf("package %q not found", consumer)
	}
	d, ok := g.GetPackage(dep)
	if !ok {
		return "", fmt.Errorf("package %q not found", dep)
	}
	targetDir := d.Path
	if version != "" {
		targetDir = filepath.Join(SnapshotDir(d.Name, version), d.Path)
	}
	rel, err := filepath.Rel(c.Path, targetDir)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	cdir := filepath.Join(root, c.Path)
	switch c.Language {
	case graph.LangGo:
		// Go merges the replace directives of all go.work members, so a module that pins a
		// release cannot stay in the root workspace. It gets its own go.work ("use .") so
		// plain go commands in it (IDE, scripts, CI) resolve natively to the snapshot.
		setGoWorkMember(root, c.Path, version == "")
		setOwnGoWork(root, cdir, version != "")
		return repointGo(cdir, root, d, rel)
	case graph.LangJavaScript, graph.LangTypeScript:
		return repointNPM(cdir, root, d.Name, rel, version)
	case graph.LangRust:
		return repointCargo(cdir, d.Name, rel, version)
	case graph.LangPython:
		return repointPython(cdir, d.Name, rel)
	}
	return "", fmt.Errorf("pinning is not supported for %s packages", c.Language)
}

func repointGo(cdir, root string, d *graph.PackageNode, rel string) (string, error) {
	gomod := filepath.Join(cdir, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	modPath := goModulePath(filepath.Join(root, d.Path, "go.mod"))
	if modPath == "" {
		// Not projected into this sparse worktree: read it from git.
		if out, err := git(root, "show", "HEAD:"+filepath.ToSlash(filepath.Join(d.Path, "go.mod"))); err == nil {
			for _, l := range strings.Split(out, "\n") {
				if f := strings.Fields(l); len(f) >= 2 && f[0] == "module" {
					modPath = strings.Trim(f[1], `"`)
				}
			}
		}
	}
	if modPath == "" {
		return "", fmt.Errorf("cannot read module path of %s", d.Name)
	}
	s := string(data)
	rep := regexp.MustCompile(`(?m)^(\s*(?:replace\s+)?)` + regexp.QuoteMeta(modPath) + `(\s+v[^\s]+)?\s*=>\s*\S+.*$`)
	line := "${1}" + modPath + " => " + rel
	if rep.MatchString(s) {
		s = rep.ReplaceAllString(s, line)
	} else {
		s = strings.TrimRight(s, "\n") + "\n\nreplace " + modPath + " => " + rel + "\n"
	}
	// The require version is left alone: the replace target (…/<slug>@<version>/…) selects
	// the code, and Go rejects v2+ requires for module paths without a /vN suffix.
	return gomod, os.WriteFile(gomod, []byte(s), 0644)
}

func goModulePath(gomod string) string {
	for _, l := range strings.Split(readFile(gomod), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

func repointNPM(cdir, root, depName, rel, version string) (string, error) {
	pj := filepath.Join(cdir, "package.json")
	data, err := os.ReadFile(pj)
	if err != nil {
		return "", err
	}
	spec := "file:" + rel
	if version == "" {
		spec = "*"
		if _, err := os.Stat(filepath.Join(root, "pnpm-lock.yaml")); err == nil {
			spec = "workspace:*"
		}
	}
	// Rewrite the value in place to keep the author's formatting and key order.
	re := regexp.MustCompile(`("` + regexp.QuoteMeta(depName) + `"\s*:\s*)"[^"]*"`)
	if !re.Match(data) {
		return "", fmt.Errorf("%s does not depend on %s", pj, depName)
	}
	out := re.ReplaceAll(data, []byte(`${1}"`+spec+`"`))
	var check map[string]any
	if json.Unmarshal(out, &check) != nil {
		return "", fmt.Errorf("refusing to write invalid JSON to %s", pj)
	}
	return pj, os.WriteFile(pj, out, 0644)
}

func repointCargo(cdir, depName, rel, version string) (string, error) {
	ct := filepath.Join(cdir, "Cargo.toml")
	data, err := os.ReadFile(ct)
	if err != nil {
		return "", err
	}
	s := string(data)
	inline := regexp.MustCompile(`(?m)^(` + regexp.QuoteMeta(depName) + `\s*=\s*\{[^}\n]*\bpath\s*=\s*)"[^"]*"`)
	if !inline.MatchString(s) {
		return "", fmt.Errorf("%s has no path dependency on %s", ct, depName)
	}
	s = inline.ReplaceAllString(s, `${1}"`+rel+`"`)
	if version != "" {
		ver := regexp.MustCompile(`(?m)^(` + regexp.QuoteMeta(depName) + `\s*=\s*\{[^}\n]*\bversion\s*=\s*)"[^"]*"`)
		s = ver.ReplaceAllString(s, `${1}"=`+version+`"`)
	}
	return ct, os.WriteFile(ct, []byte(s), 0644)
}

func repointPython(cdir, depName, rel string) (string, error) {
	pp := filepath.Join(cdir, "pyproject.toml")
	data, err := os.ReadFile(pp)
	if err != nil {
		return "", err
	}
	s := string(data)
	re := regexp.MustCompile(`(?m)^(["']?` + regexp.QuoteMeta(depName) + `["']?\s*=\s*\{[^}\n]*\bpath\s*=\s*)"[^"]*"`)
	if !re.MatchString(s) {
		return "", fmt.Errorf("%s has no [tool.uv.sources] path entry for %s", pp, depName)
	}
	s = re.ReplaceAllString(s, `${1}"`+rel+`"`)
	return pp, os.WriteFile(pp, []byte(s), 0644)
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// setGoWorkMember adds or removes a module directory from go.work's use list.
func setGoWorkMember(root, modPath string, member bool) {
	gw := filepath.Join(root, "go.work")
	data, err := os.ReadFile(gw)
	if err != nil {
		return
	}
	entry := "./" + strings.TrimPrefix(filepath.ToSlash(modPath), "./")
	lines := strings.Split(string(data), "\n")
	var out []string
	present := false
	for _, l := range lines {
		t := strings.Trim(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "use ")), `"`)
		if t == entry || t == strings.TrimPrefix(entry, "./") {
			present = true
			if !member {
				continue
			}
		}
		out = append(out, l)
	}
	if member && !present {
		for i, l := range out {
			if strings.TrimSpace(l) == "use (" {
				out = append(out[:i+1], append([]string{"\t" + entry}, out[i+1:]...)...)
				break
			}
		}
	}
	_ = os.WriteFile(gw, []byte(strings.Join(out, "\n")), 0644)
}

// setOwnGoWork creates (or removes) a module-local go.work isolating a pinned module from
// the root workspace. Only done when the repo uses a root go.work at all.
func setOwnGoWork(root, moduleDir string, own bool) {
	local := filepath.Join(moduleDir, "go.work")
	if !own {
		if strings.Contains(readFile(local), "MonoFlow") {
			_ = os.Remove(local)
		}
		return
	}
	rootWork := readFile(filepath.Join(root, "go.work"))
	if rootWork == "" {
		return
	}
	goLine := "go 1.22"
	for _, l := range strings.Split(rootWork, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "go ") {
			goLine = strings.TrimSpace(l)
			break
		}
	}
	_ = os.WriteFile(local, []byte("// MonoFlow: this module pins internal releases, so it builds outside the root\n// workspace. Managed by 'monoflow version pin/unpin'.\n"+goLine+"\n\nuse .\n"), 0644)
}
