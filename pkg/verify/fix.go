package verify

import (
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
)

// Formatting is MonoFlow's job, not the agent's: verify and ship run each package's fix_cmd
// (the repo's own formatter, set per package in .agents/monoflow.config.json) on the files the
// agent changed, and report what changed. Agents otherwise spend calls running prettier/gofmt
// themselves, or fail lint on whitespace (trpc: a Haiku run checked and re-wrote formatting by
// hand before verify).
//
// fix_cmd placeholders: {files} is the package's changed files, {files:.go} or {files:.ts,.tsx}
// only those with the given extensions; paths are absolute and quoted. Without a placeholder the
// command runs as is (e.g. "pnpm run format"), once, when the package has changed files. The
// command runs in the package directory. "none" turns fixing off for a package.

var filesPlaceholder = regexp.MustCompile(`\{files(?::([^}]*))?\}`)

// FixResult is what FixChanged did.
type FixResult struct {
	Fixed  []string // repo-relative files whose content the fix commands changed
	Failed []string // "<package>: <first line of output>" for fix commands that failed
}

// FixChanged runs the fix_cmd of every package owning one of changed (repo-relative, existing
// files), on those files only.
func FixChanged(root string, g *graph.DependencyGraph, changed []string) FixResult {
	var res FixResult
	byPkg := map[*graph.PackageNode][]string{}
	for _, f := range changed {
		if fi, err := os.Stat(filepath.Join(root, f)); err != nil || fi.IsDir() {
			continue // deleted
		}
		if p := g.MapFileToPackage(f); p != nil && p.FixCmd != "" && p.FixCmd != "none" {
			byPkg[p] = append(byPkg[p], f)
		}
	}
	pkgs := make([]*graph.PackageNode, 0, len(byPkg))
	for p := range byPkg {
		pkgs = append(pkgs, p)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })

	for _, p := range pkgs {
		files := byPkg[p]
		cmdStr, ok := expandFiles(p.FixCmd, root, files)
		if !ok {
			continue // no changed file of the kinds this command formats
		}
		before := hashFiles(root, files)
		cmd := exec.Command("sh", "-c", cmdStr)
		cmd.Dir = filepath.Join(root, p.Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			first := strings.TrimSpace(StripANSI(string(out)))
			if i := strings.Index(first, "\n"); i >= 0 {
				first = first[:i]
			}
			res.Failed = append(res.Failed, p.Name+": "+first)
			continue
		}
		after := hashFiles(root, files)
		for _, f := range files {
			if before[f] != after[f] {
				res.Fixed = append(res.Fixed, f)
			}
		}
	}
	sort.Strings(res.Fixed)
	return res
}

// expandFiles substitutes the {files} placeholders; ok=false when a placeholder matches none of
// the files.
func expandFiles(cmd, root string, files []string) (string, bool) {
	if !filesPlaceholder.MatchString(cmd) {
		return cmd, len(files) > 0
	}
	ok := true
	out := filesPlaceholder.ReplaceAllStringFunc(cmd, func(m string) string {
		var exts []string
		if sub := filesPlaceholder.FindStringSubmatch(m); sub[1] != "" {
			for _, e := range strings.Split(sub[1], ",") {
				exts = append(exts, strings.TrimSpace(e))
			}
		}
		var q []string
		for _, f := range files {
			if len(exts) > 0 && !hasExt(f, exts) {
				continue
			}
			abs := filepath.Join(root, f)
			q = append(q, "'"+strings.ReplaceAll(abs, "'", `'\''`)+"'")
		}
		if len(q) == 0 {
			ok = false
		}
		return strings.Join(q, " ")
	})
	return out, ok
}

func hasExt(f string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(f, e) {
			return true
		}
	}
	return false
}

func hashFiles(root string, files []string) map[string][32]byte {
	h := make(map[string][32]byte, len(files))
	for _, f := range files {
		if data, err := os.ReadFile(filepath.Join(root, f)); err == nil {
			h[f] = sha256.Sum256(data)
		}
	}
	return h
}
