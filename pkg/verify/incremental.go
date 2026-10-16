package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
	"github.com/antigravity/monoflow/pkg/graph"
)

// Test-file-level incremental tests (vitest). Vitest runs every test file in isolation, so a
// file that passed still passes while its own content (and snapshot), the package's other
// files, its upstream packages and the command are unchanged. A re-verify after the agent
// edited a few test files then runs only those and the files that failed, not the package's
// whole suite (trpc: re-verifies re-ran 42 s of @trpc/openapi tests for two edited files).
// Together the per-file passes cover the suite exactly, so the package pass they add up to is
// cached like a full run (local CI reuses it).

// vitestInvocation is how a package's test command runs vitest, split so that a run can be
// narrowed to some files and report every file's result.
type vitestInvocation struct {
	prefix string   // shell up to and including "vitest run"
	args   []string // arguments without the package filter
	filter string   // positional argument selecting the package ("" if none)
	root   bool     // runs from the repository root (file paths repo-relative), else the package dir
}

var vitestScript = regexp.MustCompile(`^vitest(\s+run)?((?:\s+[^\s&|;<>$` + "`" + `'"]+)*)\s*$`)

// parseVitest recognizes the two shapes discovery produces: the repo's root vitest run with a
// package filter, and a package script (`pnpm run test:lib`) whose script is plain vitest.
func parseVitest(pkg *graph.PackageNode, c, rootDir string) (vitestInvocation, bool) {
	if m := testRunnerCall.FindStringSubmatch(c); m != nil && m[2] == "vitest run" && strings.HasPrefix(c, repoRootPrefix) {
		inv := vitestInvocation{prefix: repoRootPrefix + m[1] + " vitest run", root: true}
		rest := strings.Fields(strings.TrimPrefix(c, m[0]))
		for _, a := range rest {
			if strings.ContainsAny(a, "&|;<>$`'\"") {
				return vitestInvocation{}, false
			}
			if !strings.HasPrefix(a, "-") {
				if inv.filter != "" || path.Clean(a) != path.Clean(filepath.ToSlash(pkg.Path)) {
					return vitestInvocation{}, false // a flag value or a second filter: not ours to rewrite
				}
				inv.filter = a
				continue
			}
			inv.args = append(inv.args, a)
		}
		return inv, inv.filter != ""
	}
	fields := strings.Fields(c)
	if len(fields) != 3 || fields[1] != "run" {
		return vitestInvocation{}, false
	}
	exec := map[string]string{"pnpm": "pnpm exec", "npm": "npx", "yarn": "yarn", "bun": "bunx"}[fields[0]]
	if exec == "" {
		return vitestInvocation{}, false
	}
	data, err := os.ReadFile(filepath.Join(rootDir, pkg.Path, "package.json"))
	if err != nil {
		return vitestInvocation{}, false
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return vitestInvocation{}, false
	}
	m := vitestScript.FindStringSubmatch(strings.TrimSpace(manifest.Scripts[fields[2]]))
	if m == nil {
		return vitestInvocation{}, false
	}
	inv := vitestInvocation{prefix: exec + " vitest run"}
	for _, a := range strings.Fields(m[2]) {
		if !strings.HasPrefix(a, "-") {
			return vitestInvocation{}, false // a filter or a flag value
		}
		inv.args = append(inv.args, a)
	}
	return inv, true
}

// command runs the given files (all of the package's tests when files is nil) and writes
// every file's result as JSON to report.
func (v vitestInvocation) command(files []string, report string) string {
	parts := []string{v.prefix}
	parts = append(parts, v.args...)
	parts = append(parts, "--reporter=default", "--reporter=json", "--outputFile.json="+shellQuote(report))
	if files == nil {
		if v.filter != "" {
			parts = append(parts, v.filter)
		}
	} else {
		for _, f := range files {
			parts = append(parts, shellQuote(f))
		}
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// vitestIsolationOff reports a vitest config sharing state between test files (isolate: false):
// then a file's result can depend on the others, and only full runs count.
func vitestIsolationOff(dirs ...string) bool {
	for _, d := range dirs {
		for _, pat := range []string{"vite.config.*", "vitest.config.*", "vitest.workspace.*"} {
			matches, _ := filepath.Glob(filepath.Join(d, pat))
			for _, f := range matches {
				if data, err := os.ReadFile(f); err == nil && regexp.MustCompile(`isolate\s*:\s*false`).Match(data) {
					return true
				}
			}
		}
	}
	return false
}

// testManifest records a full run: every test file present and its hash, for one state of
// the package's other inputs.
type testManifest struct {
	Files map[string]string `json:"files"` // package-relative test file -> content hash
	Ran   []string          `json:"ran"`   // the files the full run executed
}

// runTestsIncremental runs a vitest test command for pkg, narrowed to the test files without
// a pass on their current inputs. handled=false: not applicable, run c as usual.
func (r *Runner) runTestsIncremental(pkg *graph.PackageNode, c, absPkgPath string, opt VerifyOptions) (out []byte, err error, handled bool) {
	if r.cacheMgr == nil || opt.NoCache || os.Getenv("MONOFLOW_NO_INCREMENTAL_TESTS") != "" {
		return nil, nil, false
	}
	// -u rewrites snapshots: only in the files without a pass on their inputs (the ones that can
	// mismatch), and records nothing (the files change under the run; the next verify keys them).
	update := opt.UpdateSnapshots
	if update {
		if !strings.HasSuffix(c, " -u") {
			return nil, nil, false
		}
		c = strings.TrimSuffix(c, " -u")
	}
	inv, ok := parseVitest(pkg, c, r.RootDir)
	if !ok || vitestIsolationOff(absPkgPath, r.RootDir) {
		return nil, nil, false
	}
	srcKey, kerr := r.cacheMgr.ComputePackageKey(pkg, r.upstreamKeys(pkg), c, "test-sources")
	if kerr != nil || srcKey == "" {
		return nil, nil, false
	}
	current := currentTestFiles(absPkgPath)
	fileKey := func(f string) string {
		h := sha256.New()
		fmt.Fprintf(h, "%s\n%s\n%s\n", srcKey, f, current[f])
		return hex.EncodeToString(h.Sum(nil))
	}
	var manifest testManifest
	full := true
	if rec, hit := r.cacheMgr.PeekVerification(manifestKey(srcKey)); hit && json.Unmarshal([]byte(rec.Output), &manifest) == nil {
		full = false
		for f, h := range current {
			if old, known := manifest.Files[f]; !known || (old != h && !contains(manifest.Ran, f)) {
				full = true // a new test file, or an edited one the suite does not run (it may be imported)
				break
			}
		}
	}
	if update {
		inv.args = append(inv.args, "-u")
		if full {
			return nil, nil, false
		}
	}
	var pending []string
	if !full {
		for _, f := range manifest.Ran {
			if _, exists := current[f]; !exists {
				continue // deleted: nothing left to run
			}
			if rec, hit := r.cacheMgr.PeekVerification(fileKey(f)); !hit || rec.Status != "passed" {
				pending = append(pending, f)
			}
		}
		if len(pending) == 0 {
			return []byte("all test files passed on these inputs\n"), nil, true
		}
		sort.Strings(pending)
	}

	report, ferr := os.CreateTemp("", "monoflow-vitest-*.json")
	if ferr != nil {
		return nil, nil, false
	}
	_ = report.Close()
	defer os.Remove(report.Name())
	var cmd string
	if full {
		cmd = inv.command(nil, report.Name())
	} else {
		files := make([]string, len(pending))
		for i, f := range pending {
			files[i] = f
			if inv.root {
				files[i] = path.Join(filepath.ToSlash(pkg.Path), f)
			}
		}
		cmd = inv.command(files, report.Name())
	}
	out, err = r.runCheck(cmd, absPkgPath)
	if update {
		return out, err, true
	}

	results, rerr := readVitestReport(report.Name(), absPkgPath)
	if rerr != nil || len(results) == 0 {
		return out, err, true // no per-file results (crashed, or no tests): nothing recorded
	}
	for f, passed := range results {
		if _, ok := current[f]; ok && passed {
			_ = r.cacheMgr.PutVerification(&cache.VerificationRecord{
				Key: fileKey(f), Target: pkg.Name, Language: string(pkg.Language), Status: "passed",
				CommandRun: c + " (" + f + ")", Timestamp: time.Now().UTC(),
			})
		}
	}
	if full {
		m := testManifest{Files: current}
		for f := range results {
			m.Ran = append(m.Ran, f)
		}
		sort.Strings(m.Ran)
		data, _ := json.Marshal(m)
		_ = r.cacheMgr.PutVerification(&cache.VerificationRecord{
			Key: manifestKey(srcKey), Target: pkg.Name, Language: string(pkg.Language), Status: "manifest",
			CommandRun: c, Timestamp: time.Now().UTC(), Output: string(data),
		})
	}
	return out, err, true
}

func manifestKey(srcKey string) string {
	h := sha256.Sum256([]byte("test-manifest\n" + srcKey))
	return hex.EncodeToString(h[:])
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// currentTestFiles maps the package's test files (package-relative) to a hash of their
// content and their snapshot file.
func currentTestFiles(absPkgPath string) map[string]string {
	files := map[string]string{}
	for _, f := range cache.SourceFiles(absPkgPath) {
		if !jsTestFile.MatchString(f) {
			continue
		}
		h := sha256.New()
		data, err := os.ReadFile(filepath.Join(absPkgPath, f))
		if err != nil {
			continue
		}
		h.Write(data)
		snap := path.Join(path.Dir(f), "__snapshots__", path.Base(f)+".snap")
		if s, err := os.ReadFile(filepath.Join(absPkgPath, snap)); err == nil {
			h.Write([]byte("\x00snapshot\x00"))
			h.Write(s)
		}
		files[f] = hex.EncodeToString(h.Sum(nil))
	}
	return files
}

// readVitestReport reads vitest's JSON report: package-relative test file -> passed.
func readVitestReport(file, absPkgPath string) (map[string]bool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var rep struct {
		TestResults []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"testResults"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, err
	}
	base := resolved(absPkgPath)
	res := map[string]bool{}
	for _, t := range rep.TestResults {
		name := resolved(t.Name)
		rel, err := filepath.Rel(base, name)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		res[filepath.ToSlash(rel)] = t.Status == "passed"
	}
	return res, nil
}

// resolved is p with symlinks resolved (macOS: /tmp is /private/tmp), or p when it cannot be.
// The longest existing prefix is resolved, so a path that does not exist (yet) still compares.
func resolved(p string) string {
	rest := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(dir) == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}
