package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/antigravity/monoflow/pkg/graph"
)

// classifyPreexisting re-runs each failing check on the merge-base. A failure whose every
// failing line also occurs there was not caused by this change: it is moved to
// Preexisting and no longer blocks. This lets MonoFlow plug into existing repos whose main
// branch is not perfectly clean without masking new breakage in the same package.
func (r *Runner) classifyPreexisting(g *graph.DependencyGraph, results []TargetResult, opt VerifyOptions) {
	var failed []int
	for i, res := range results {
		if res.Status == "failed" && res.CommandRun != "" && len(res.Errors) > 0 && res.Errors[0].Code != "package_not_projected" {
			failed = append(failed, i)
		}
	}
	if len(failed) == 0 {
		return
	}
	baseDir := r.baselineDir(opt.BaseRef)
	if baseDir == "" {
		return
	}
	// The baseline needs the same dependency builds as the checks being compared (memoised).
	var failedPkgs []*graph.PackageNode
	for _, i := range failed {
		if pkg, ok := g.GetPackage(results[i].Target); ok {
			failedPkgs = append(failedPkgs, pkg)
		}
	}
	for _, c := range PrepareCommands(g, failedPkgs) {
		runBaselineCheck(baseDir, baseDir, c)
	}
	var wg sync.WaitGroup
	for _, i := range failed {
		pkg, ok := g.GetPackage(results[i].Target)
		if results[i].Target == PrepareTarget {
			pkg, ok = &graph.PackageNode{Path: "."}, true // a failed dependency build: compare at the root
		}
		if !ok {
			continue
		}
		wg.Add(1)
		go func(res *TargetResult, pkgPath string) {
			defer wg.Done()
			dir := filepath.Join(baseDir, pkgPath)
			if _, err := os.Stat(dir); err != nil {
				return // package is new in this change: nothing to compare against
			}
			baseOut, baseFailed := runBaselineCheck(baseDir, dir, res.CommandRun)
			if !baseFailed {
				return
			}
			base := unroot(unroot(baseOut, baseDir), r.RootDir) // its dependencies link into the root
			if !errorsInBaseline(res.Errors, base, r.RootDir) {
				// Some failures are old, some new: block on the new ones only, note the rest.
				res.Errors, res.Preexisting = splitByBaseline(res.Errors, base, r.RootDir)
				return
			}
			res.Status = "passed"
			res.Preexisting = res.Errors
			res.Errors = make([]DiagnosticError, 0)
			// The run stopped at that (old) failure, so later checks never ran: a preexisting
			// lint finding must not hide a new test failure. Run them now, judging each the same way.
			r.runRemaining(res, baseDir, dir)
		}(&results[i], pkg.Path)
	}
	wg.Wait()
}

// runRemaining runs the checks skipped after a preexisting failure. A new failure blocks
// with its own diagnostics; another preexisting one is noted and the next check runs.
func (r *Runner) runRemaining(res *TargetResult, baseDir, basePkgDir string) {
	for len(res.remaining) > 0 {
		c := res.remaining[0]
		res.remaining = res.remaining[1:]
		out, err := r.runCheck(c, res.dir)
		if err == nil {
			continue
		}
		errs := ParseDiagnostics(string(out))
		ResolveGoTestFiles(errs, res.dir)
		if len(errs) == 0 {
			errs = []DiagnosticError{{File: ".", Line: 1, Code: "execution_failure", Message: CompactFailure(StripANSI(string(out)))}}
		}
		baseOut, baseFailed := runBaselineCheck(baseDir, basePkgDir, c)
		base := unroot(unroot(baseOut, baseDir), r.RootDir) // its dependencies link into the root
		if baseFailed && errorsInBaseline(errs, base, r.RootDir) {
			res.Preexisting = append(res.Preexisting, errs...)
			continue
		}
		var old []DiagnosticError
		if baseFailed {
			errs, old = splitByBaseline(errs, base, r.RootDir)
		}
		res.Status, res.Errors, res.CommandRun = "failed", errs, c
		res.Preexisting = append(res.Preexisting, old...)
		return
	}
}

// baselineDir returns (creating once) a detached checkout of the merge-base of HEAD and
// the base branch, shared by all worktrees through the package cache directory.
// baselineSHA is the commit failures are compared against: the merge-base with the base
// branch, or HEAD's parent when HEAD is on the base branch with no local edits.
func (r *Runner) baselineSHA(baseRef string) string {
	cands := []string{"origin/main", "main", "origin/master", "master"}
	if baseRef != "" {
		cands = []string{baseRef}
	}
	var mb string
	for _, c := range cands {
		out, err := exec.Command("git", "-C", r.RootDir, "merge-base", c, "HEAD").Output()
		if err == nil {
			mb = strings.TrimSpace(string(out))
			break
		}
	}
	head, _ := exec.Command("git", "-C", r.RootDir, "rev-parse", "HEAD").Output()
	if mb == "" {
		return ""
	}
	if mb == strings.TrimSpace(string(head)) && !worktreeDirty(r.RootDir) {
		// HEAD is on the base branch with no local edits: the change under test is HEAD
		// itself, so the baseline is its parent (never the commit being judged).
		parent, err := exec.Command("git", "-C", r.RootDir, "rev-parse", "HEAD^").Output()
		if err != nil {
			return "" // root commit: no baseline, failures are real
		}
		mb = strings.TrimSpace(string(parent))
	}
	return mb
}

func (r *Runner) baselineDir(baseRef string) string {
	mb := r.baselineSHA(baseRef)
	if mb == "" || r.cacheMgr == nil {
		return ""
	}
	dir := filepath.Join(r.cacheMgr.LocalDir, "baselines", mb)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if _, err := os.Lstat(filepath.Join(dir, "node_modules")); err != nil {
			// Made before dependencies were linked: link them and drop results memoised without them.
			graph.PrepareNodeModules(r.RootDir, dir)
			if _, err := os.Lstat(filepath.Join(dir, "node_modules")); err == nil {
				memos, _ := filepath.Glob(filepath.Join(dir, ".monoflow-baseline-*"))
				for _, m := range memos {
					_ = os.Remove(m)
				}
			}
		}
		return dir
	}
	_ = os.MkdirAll(filepath.Dir(dir), 0755)
	pruneBaselines(r.RootDir, filepath.Dir(dir), 2)
	if out, err := exec.Command("git", "-C", r.RootDir, "worktree", "add", "--detach", dir, mb).CombinedOutput(); err != nil {
		_ = out
		return ""
	}
	// Without dependencies every JS check fails differently on the baseline, so old failures
	// all looked new (trpc: 40 of 48 packages "failed" on a one-line change).
	graph.PrepareNodeModules(r.RootDir, dir)
	return dir
}

// worktreeDirty reports uncommitted changes, ignoring MonoFlow's own .agents/ files.
func worktreeDirty(dir string) bool {
	out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	for _, l := range strings.Split(string(out), "\n") {
		if len(l) > 3 && !strings.HasPrefix(strings.TrimSpace(l[3:]), ".agents/") {
			return true
		}
	}
	return false
}

// runBaselineCheck runs cmd in dir on the baseline checkout, memoising the result per
// command+package in the baseline (the baseline commit never changes).
func runBaselineCheck(baseDir, dir, cmdStr string) (string, bool) {
	sum := sha256.Sum256([]byte(dir + "\x00" + cmdStr))
	// v2: full output (v1 kept only the last 1500 chars, so packages with many preexisting
	// errors, e.g. 610 type errors in a trpc example, never matched and blocked as "new").
	memo := filepath.Join(baseDir, ".monoflow-baseline-v2-"+hex.EncodeToString(sum[:8])+".json")
	var rec struct {
		Output string `json:"output"`
		Failed bool   `json:"failed"`
	}
	if data, err := os.ReadFile(memo); err == nil && json.Unmarshal(data, &rec) == nil {
		return rec.Output, rec.Failed
	}
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	rec.Output, rec.Failed = StripANSI(string(out)), err != nil
	if len(rec.Output) > 4<<20 {
		rec.Output = rec.Output[len(rec.Output)-4<<20:]
	}
	if data, err := json.Marshal(rec); err == nil {
		_ = os.WriteFile(memo, data, 0644)
	}
	return rec.Output, rec.Failed
}

// volatile matches run-specific text: durations, clock times, timestamps and ids (10+ digits,
// e.g. tsx's "index.mjs?1791714506271"), addresses and checkout paths.
var volatile = regexp.MustCompile(`\(\d+(\.\d+)?m?s\)|\d+(\.\d+)?m?s\b|\b\d{1,2}:\d{2}:\d{2}\b|\d{10,}|0x[0-9a-f]+|/[^\s:]*/(baselines/[0-9a-f]+|worktrees/[^/\s]+)/`)

// failureLinesSubset reports whether every meaningful failing line of current also
// appears in base (after removing timings, addresses and checkout paths).
func failureLinesSubset(current, base string) bool {
	norm := func(s string) string { return strings.TrimSpace(volatile.ReplaceAllString(s, "")) }
	baseSet := map[string]bool{}
	for _, l := range strings.Split(base, "\n") {
		baseSet[norm(l)] = true
	}
	baseText := norm(base)
	lines := strings.Split(current, "\n")
	if strings.HasPrefix(current, "…") && len(lines) > 1 {
		lines = lines[1:] // CompactFailure cut this line mid-way: it can't match a whole line
	}
	for _, l := range lines {
		n := norm(l)
		if n == "" || n == "FAIL" || strings.HasPrefix(n, "exit status") || strings.HasPrefix(n, "go: downloading") {
			continue
		}
		if !baseSet[n] && !strings.Contains(baseText, n) {
			return false
		}
	}
	return true
}

// errorsInBaseline reports whether every current error also occurs in the baseline output:
// raw output lines for unstructured failures; file name and message for diagnostics.
// unroot replaces a checkout's absolute path (as given and symlink-resolved) with a placeholder,
// so messages naming files by absolute path (tsc: import("/repo/node_modules/...")) compare
// equal between the working checkout and the baseline checkout.
func unroot(s, root string) string {
	if root == "" {
		return s
	}
	paths := []string{root}
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		paths = append(paths, real)
	}
	for _, p := range paths {
		s = strings.ReplaceAll(s, p, "<root>")
		if q, ok := strings.CutPrefix(p, "/private"); ok { // macOS /tmp -> /private/tmp
			s = strings.ReplaceAll(s, q, "<root>")
		}
	}
	return s
}

// splitByBaseline separates failures that are new from those the baseline also has.
// Unstructured failures cannot be split: they stay new.
func splitByBaseline(errs []DiagnosticError, base, root string) (fresh, old []DiagnosticError) {
	for _, e := range errs {
		if e.Code != "execution_failure" && errorsInBaseline([]DiagnosticError{e}, base, root) {
			old = append(old, e)
		} else {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 { // cannot happen when the whole set did not match; keep it blocking
		return errs, nil
	}
	return fresh, old
}

func errorsInBaseline(errs []DiagnosticError, base, root string) bool {
	if root != "" {
		cur := make([]DiagnosticError, len(errs))
		for i, e := range errs {
			e.Message = unroot(e.Message, root)
			e.File = unroot(e.File, root)
			cur[i] = e
		}
		errs = cur
	}
	norm := func(s string) string { return strings.TrimSpace(volatile.ReplaceAllString(s, "")) }
	baseText := norm(base)
	// Diagnostics assembled from several lines (test name + error + expected/received) never
	// appear verbatim in raw output: compare them with the baseline's parsed diagnostics too.
	var baseDiags map[string]bool
	for _, e := range errs {
		if e.Code == "execution_failure" {
			if !failureLinesSubset(e.Message, base) {
				return false
			}
			continue
		}
		if strings.Contains(baseText, filepath.Base(e.File)) && strings.Contains(baseText, norm(e.Message)) {
			continue
		}
		if baseDiags == nil {
			baseDiags = map[string]bool{}
			for _, b := range ParseDiagnostics(base) {
				baseDiags[filepath.Base(b.File)+"\x00"+norm(b.Message)] = true
			}
		}
		if !baseDiags[filepath.Base(e.File)+"\x00"+norm(e.Message)] {
			return false
		}
	}
	return true
}

// pruneBaselines keeps only the most recently used baseline checkouts.
func pruneBaselines(root, dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) < keep {
		return
	}
	type item struct {
		path string
		mod  int64
	}
	var items []item
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() {
			items = append(items, item{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
		}
	}
	for len(items) >= keep {
		oldest := 0
		for i := range items {
			if items[i].mod < items[oldest].mod {
				oldest = i
			}
		}
		_ = exec.Command("git", "-C", root, "worktree", "remove", "--force", items[oldest].path).Run()
		_ = os.RemoveAll(items[oldest].path)
		items = append(items[:oldest], items[oldest+1:]...)
	}
}
