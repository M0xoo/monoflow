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
	var wg sync.WaitGroup
	for _, i := range failed {
		pkg, ok := g.GetPackage(results[i].Target)
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
			if errorsInBaseline(res.Errors, baseOut) {
				res.Status = "passed"
				res.Preexisting = res.Errors
				res.Errors = make([]DiagnosticError, 0)
			}
		}(&results[i], pkg.Path)
	}
	wg.Wait()
}

// baselineDir returns (creating once) a detached checkout of the merge-base of HEAD and
// the base branch, shared by all worktrees through the package cache directory.
func (r *Runner) baselineDir(baseRef string) string {
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
	if mb == "" || r.cacheMgr == nil {
		return ""
	}
	if mb == strings.TrimSpace(string(head)) && !worktreeDirty(r.RootDir) {
		return "" // nothing changed relative to the base: a failure is real
	}
	dir := filepath.Join(r.cacheMgr.LocalDir, "baselines", mb)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir
	}
	_ = os.MkdirAll(filepath.Dir(dir), 0755)
	if out, err := exec.Command("git", "-C", r.RootDir, "worktree", "add", "--detach", dir, mb).CombinedOutput(); err != nil {
		_ = out
		return ""
	}
	return dir
}

func worktreeDirty(dir string) bool {
	out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	return len(strings.TrimSpace(string(out))) > 0
}

// runBaselineCheck runs cmd in dir on the baseline checkout, memoising the result per
// command+package in the baseline (the baseline commit never changes).
func runBaselineCheck(baseDir, dir, cmdStr string) (string, bool) {
	sum := sha256.Sum256([]byte(dir + "\x00" + cmdStr))
	memo := filepath.Join(baseDir, ".monoflow-baseline-"+hex.EncodeToString(sum[:8])+".json")
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
	rec.Output, rec.Failed = CompactFailure(StripANSI(string(out))), err != nil
	if data, err := json.Marshal(rec); err == nil {
		_ = os.WriteFile(memo, data, 0644)
	}
	return rec.Output, rec.Failed
}

var volatile = regexp.MustCompile(`\(\d+(\.\d+)?m?s\)|\d+(\.\d+)?m?s\b|0x[0-9a-f]+|/[^\s:]*/(baselines/[0-9a-f]+|worktrees/[^/\s]+)/`)

// failureLinesSubset reports whether every meaningful failing line of current also
// appears in base (after removing timings, addresses and checkout paths).
func failureLinesSubset(current, base string) bool {
	norm := func(s string) string { return strings.TrimSpace(volatile.ReplaceAllString(s, "")) }
	baseSet := map[string]bool{}
	for _, l := range strings.Split(base, "\n") {
		baseSet[norm(l)] = true
	}
	baseText := norm(base)
	for _, l := range strings.Split(current, "\n") {
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
func errorsInBaseline(errs []DiagnosticError, base string) bool {
	norm := func(s string) string { return strings.TrimSpace(volatile.ReplaceAllString(s, "")) }
	baseText := norm(base)
	for _, e := range errs {
		if e.Code == "execution_failure" {
			if !failureLinesSubset(e.Message, base) {
				return false
			}
			continue
		}
		if !strings.Contains(baseText, filepath.Base(e.File)) || !strings.Contains(baseText, norm(e.Message)) {
			return false
		}
	}
	return true
}
