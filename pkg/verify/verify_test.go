package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antigravity/monoflow/pkg/graph"
)

func TestStripANSI(t *testing.T) {
	colored := "\x1b[31mError:\x1b[0m \x1b[1mType mismatch\x1b[0m\r\n⠋ Running checks...\r"
	stripped := StripANSI(colored)
	expected := "Error: Type mismatch\n\nRunning checks...\n"
	if stripped != expected {
		t.Fatalf("unexpected stripped output: %q, expected: %q", stripped, expected)
	}
}

func TestParseTypeScriptDiagnostic(t *testing.T) {
	raw := `
services/billing/tax.ts:42:11 - error TS2322: Type 'string' is not assignable to type 'number'.
Found 1 error.
`
	errors := ParseDiagnostics(raw)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}

	e := errors[0]
	if e.File != "services/billing/tax.ts" {
		t.Errorf("expected file services/billing/tax.ts, got %s", e.File)
	}
	if e.Line != 42 || e.Col != 11 {
		t.Errorf("expected 42:11, got %d:%d", e.Line, e.Col)
	}
	if e.Code != "TS2322" {
		t.Errorf("expected TS2322, got %s", e.Code)
	}
	if e.Expected != "number" || e.Received != "string" {
		t.Errorf("expected expected=number, received=string, got expected=%s, received=%s", e.Expected, e.Received)
	}
}

func TestParseGoDiagnostic(t *testing.T) {
	raw := `
services/auth/auth.go:77:12: cannot use token (variable of type string) as []byte value in argument
`
	errors := ParseDiagnostics(raw)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	e := errors[0]
	if e.File != "services/auth/auth.go" || e.Line != 77 || e.Col != 12 {
		t.Errorf("unexpected file/line/col: %s:%d:%d", e.File, e.Line, e.Col)
	}
}

func TestParsePythonDiagnostic(t *testing.T) {
	raw := `
services/ml/pipeline.py:42:5: F821 Undefined name 'preprocess_image'
`
	errors := ParseDiagnostics(raw)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	e := errors[0]
	if e.File != "services/ml/pipeline.py" || e.Line != 42 || e.Col != 5 || e.Code != "F821" {
		t.Errorf("unexpected python error parse: %+v", e)
	}
}

func TestParseRustDiagnostic(t *testing.T) {
	raw := `
error[E0308]: mismatched types
 --> packages/crypto/src/hasher.rs:28:14
`
	errors := ParseDiagnostics(raw)
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}
	e := errors[0]
	if e.File != "packages/crypto/src/hasher.rs" || e.Line != 28 || e.Col != 14 || e.Code != "E0308" {
		t.Errorf("unexpected rust error parse: %+v", e)
	}
}

func TestQuarantineFlakyTest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_quar_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	qm := NewQuarantineManager(tmpDir)
	err = qm.Quarantine("services/billing/test_tax.py:test_flaky_gateway", "intermittent network timeout", "timeout after 5000ms")
	if err != nil {
		t.Fatalf("failed to quarantine: %v", err)
	}

	if !qm.IsQuarantined("services/billing/test_tax.py:test_flaky_gateway") {
		t.Errorf("expected test to be quarantined")
	}

	if qm.IsQuarantined("services/billing/test_tax.py:test_healthy_calculation") {
		t.Errorf("unexpected quarantine match for healthy test")
	}
}

func TestVerifyCachingFastIterativeLoop(t *testing.T) {
	temp := t.TempDir()
	pkgDir := filepath.Join(temp, "services", "billing")
	_ = os.MkdirAll(pkgDir, 0755)
	testFile := filepath.Join(pkgDir, "test.sh")
	_ = os.WriteFile(testFile, []byte("#!/bin/sh\necho 'running unit test'\nexit 0\n"), 0755)

	pkg := &graph.PackageNode{
		Name:     "services/billing",
		Path:     "services/billing",
		Language: graph.LangTypeScript,
		TestCmd:  "sh test.sh",
	}

	runner := NewRunner(temp)

	// 1. Initial run: Cache Miss -> Command executed
	res1 := runner.VerifyTarget(pkg, VerifyOptions{Mode: "test"})
	if res1.Status != "passed" {
		t.Fatalf("res1 expected passed, got %s: %+v", res1.Status, res1.Errors)
	}
	if res1.Cached {
		t.Errorf("res1 expected Cached=false on first run")
	}

	// 2. Second run: Cache HIT -> returns in < 1ms
	res2 := runner.VerifyTarget(pkg, VerifyOptions{Mode: "test"})
	if res2.Status != "passed" {
		t.Fatalf("res2 expected passed")
	}
	if !res2.Cached {
		t.Errorf("res2 expected Cached=true on repeat run with unchanged code")
	}

	// 3. Force bypass cache: NoCache=true
	res3 := runner.VerifyTarget(pkg, VerifyOptions{Mode: "test", NoCache: true})
	if res3.Cached {
		t.Errorf("res3 expected Cached=false when NoCache is true")
	}

	// 4. Modify test file -> Cache invalidated!
	_ = os.WriteFile(testFile, []byte("#!/bin/sh\necho 'running modified unit test'\nexit 0\n"), 0755)
	res4 := runner.VerifyTarget(pkg, VerifyOptions{Mode: "test"})
	if res4.Cached {
		t.Errorf("res4 expected Cached=false after code modification")
	}
}

// Dependency builds are merged into one runner call per command naming every upstream package.
func TestPrepareCommandsMergeUpstreams(t *testing.T) {
	g := graph.New(t.TempDir())
	turbo := "pnpm exec turbo run build --output-logs=errors-only --log-prefix=none"
	g.AddPackage(&graph.PackageNode{Name: "@t/core", Path: "packages/core"})
	g.AddPackage(&graph.PackageNode{Name: "@t/server", Path: "packages/server", Dependencies: []string{"@t/core"}, PrepareCmd: turbo})
	g.AddPackage(&graph.PackageNode{Name: "@t/client", Path: "packages/client", Dependencies: []string{"@t/server"}, PrepareCmd: turbo})
	g.AddPackage(&graph.PackageNode{Name: "@t/leaf", Path: "packages/leaf", PrepareCmd: turbo}) // no upstream
	g.RebuildEdges()
	var pkgs []*graph.PackageNode
	for _, n := range []string{"@t/server", "@t/client", "@t/leaf"} {
		p, _ := g.GetPackage(n)
		pkgs = append(pkgs, p)
	}
	got := PrepareCommands(g, pkgs)
	want := turbo + " --filter=@t/core --filter=@t/server"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// While a changed package is broken (here: a compile error), its dependents are not run (fast
// fix loop); once it passes they are, so a pass still covers every dependent.
func TestVerifyAffectedChecksChangedPackagesFirst(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "core"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "app"), 0755)
	_ = os.WriteFile(filepath.Join(root, "core", "ok"), []byte("no\n"), 0644)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "core", Path: "core", Language: graph.LangGeneric, TestCmd: `grep -qx yes ok || { echo "./core.go:1:2: undefined: x"; exit 1; }`})
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangGeneric, Dependencies: []string{"core"}, TestCmd: "touch ran"})
	g.RebuildEdges()
	r := NewRunner(root)
	res := r.VerifyAffected(g, []string{"core/ok"}, VerifyOptions{Strict: true})
	if res.Status != "failed" || len(res.Pending) != 1 || res.Pending[0] != "app" {
		t.Fatalf("want core failed, app pending: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "ran")); err == nil {
		t.Fatalf("dependent ran while the changed package failed")
	}
	_ = os.WriteFile(filepath.Join(root, "core", "ok"), []byte("yes\n"), 0644)
	res = r.VerifyAffected(g, []string{"core/ok"}, VerifyOptions{Strict: true})
	if res.Status != "passed" || res.TotalTargets != 2 || len(res.Pending) != 0 {
		t.Fatalf("want both checked and passed: %+v", res)
	}
}

// A target that fails once and passes alone (a timeout under load) is reported as flaky, not
// failed; a real failure stays failed.
func TestFailedTargetRetriedOnceAndMarkedFlaky(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "flaky"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "broken"), 0755)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "flaky", Path: "flaky", Language: graph.LangGeneric,
		TestCmd: `if [ -f seen ]; then exit 0; fi; touch seen; echo "x_test.go:1: Test timed out in 5000ms"; exit 1`})
	g.AddPackage(&graph.PackageNode{Name: "broken", Path: "broken", Language: graph.LangGeneric,
		TestCmd: `echo "y_test.go:2: want 1"; exit 1`})
	res := NewRunner(root).VerifyPackages(g, []string{"flaky", "broken"}, VerifyOptions{Strict: true})
	got := map[string]TargetResult{}
	for _, r := range res.Results {
		got[r.Target] = r
	}
	if r := got["flaky"]; r.Status != "passed" || !r.Flaky {
		t.Fatalf("flaky target: %+v", r)
	}
	if r := got["broken"]; r.Status != "failed" || r.Flaky {
		t.Fatalf("broken target: %+v", r)
	}
}

// An assertion that fails once (a keep-alive test under load) is not retried by verify, where
// the agent waits, but is by local CI before it can block a push; a real failure stays failed.
func TestLocalCIRetriesAnyFailureOnce(t *testing.T) {
	setup := func() (*graph.DependencyGraph, string) {
		root := t.TempDir()
		_ = os.MkdirAll(filepath.Join(root, "ws"), 0755)
		_ = os.MkdirAll(filepath.Join(root, "broken"), 0755)
		g := graph.New(root)
		g.AddPackage(&graph.PackageNode{Name: "ws", Path: "ws", Language: graph.LangGeneric,
			TestCmd: `if [ -f seen ]; then exit 0; fi; touch seen; echo "ws.test.ts:1552: expected vi.fn() to be called at least once"; exit 1`})
		g.AddPackage(&graph.PackageNode{Name: "broken", Path: "broken", Language: graph.LangGeneric,
			TestCmd: `echo "y_test.go:2: want 1"; exit 1`})
		return g, root
	}
	status := func(opt VerifyOptions) map[string]TargetResult {
		g, root := setup()
		opt.Strict = true
		got := map[string]TargetResult{}
		for _, r := range NewRunner(root).VerifyPackages(g, []string{"ws", "broken"}, opt).Results {
			got[r.Target] = r
		}
		return got
	}
	if r := status(VerifyOptions{})["ws"]; r.Status != "failed" {
		t.Fatalf("verify must not retry an assertion failure: %+v", r)
	}
	got := status(VerifyOptions{RetryAllFailures: true})
	if r := got["ws"]; r.Status != "passed" || !r.Flaky {
		t.Fatalf("local CI must retry and mark flaky: %+v", r)
	}
	if r := got["broken"]; r.Status != "failed" {
		t.Fatalf("a real failure stays failed: %+v", r)
	}
}

// verify re-runs only the failed test files of a vitest check, once: a test that passes alone is
// reported flaky and does not block; one that fails again stays failed.
func TestVerifyRetriesFailedTestFilesOnce(t *testing.T) {
	root := t.TempDir()
	_ = exec.Command("git", "-C", root, "init", "-q").Run()
	for _, f := range []string{"ws/src/a.test.ts", "broken/src/b.test.ts"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0755)
		_ = os.WriteFile(filepath.Join(root, f), []byte("test"), 0644)
	}
	// A stand-in for "pnpm exec vitest": the ws test fails on its first run only.
	fake := `case "$*" in
*broken*) printf ' FAIL  |broken| src/b.test.ts > s > t\nAssertionError: want 1\n ❯ src/b.test.ts:2:1\n'; exit 1;;
esac
if [ -f "$(git rev-parse --show-toplevel)/.seen" ]; then exit 0; fi
touch "$(git rev-parse --show-toplevel)/.seen"
printf ' FAIL  |ws| src/a.test.ts > keep alive > pong\nAssertionError: expected "vi.fn()" to be called at least once\n ❯ src/a.test.ts:1552:7\n'
exit 1
`
	_ = os.WriteFile(filepath.Join(root, "vitest"), []byte(fake), 0755)
	g := graph.New(root)
	for _, name := range []string{"ws", "broken"} {
		g.AddPackage(&graph.PackageNode{Name: name, Path: name, Language: graph.LangTypeScript,
			TestCmd: `cd "$(git rev-parse --show-toplevel)" && sh vitest run --passWithNoTests ` + name})
	}
	got := map[string]TargetResult{}
	for _, r := range NewRunner(root).VerifyPackages(g, []string{"ws", "broken"}, VerifyOptions{Strict: true}).Results {
		got[r.Target] = r
	}
	if r := got["ws"]; r.Status != "passed" || !r.Flaky {
		t.Fatalf("a test file passing alone must be reported flaky, not failed: %+v", r)
	}
	if r := got["broken"]; r.Status != "failed" {
		t.Fatalf("a test failing again stays failed: %+v", r)
	}
}

// A pass with --update-snapshots is reused by the next plain verify on the files -u left: it
// used to be stored only under the "-u" command, so the plain run re-ran every package.
func TestSnapshotUpdatePassIsReusedByPlainVerify(t *testing.T) {
	root := t.TempDir()
	_ = exec.Command("git", "-C", root, "init", "-q").Run()
	_ = os.MkdirAll(filepath.Join(root, "app"), 0755)
	_ = os.WriteFile(filepath.Join(root, "app", "snap.txt"), []byte("old\n"), 0644)
	_ = os.WriteFile(filepath.Join(root, "app", "check.sh"), []byte(`if [ "$1" = "-u" ]; then echo new > snap.txt; fi; [ "$(cat snap.txt)" = new ]`), 0644)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangTypeScript, TestCmd: "sh check.sh"})

	if res := NewRunner(root).VerifyPackages(g, []string{"app"}, VerifyOptions{UpdateSnapshots: true, Strict: true}); res.Status != "passed" {
		t.Fatalf("-u run: %+v", res.Results)
	}
	res := NewRunner(root).VerifyPackages(g, []string{"app"}, VerifyOptions{Strict: true})
	if res.Status != "passed" || res.CachedTargets != 1 {
		t.Fatalf("plain verify after -u should be a cache hit: cached=%d %+v", res.CachedTargets, res.Results)
	}
}

// When the changed package fails only on test assertions (it compiles), its dependents run in
// the same verify: one round shows every test the change breaks.
func TestVerifyAffectedRunsDependentsOnTestOnlyFailures(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "core"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "app"), 0755)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "core", Path: "core", Language: graph.LangGeneric, TestCmd: `echo "--- FAIL: TestCode (0.00s)"; echo "    core_test.go:9: want 422"; echo FAIL; exit 1`})
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangGeneric, Dependencies: []string{"core"}, TestCmd: `echo "--- FAIL: TestApp (0.00s)"; echo "    app_test.go:3: want 422"; echo FAIL; exit 1`})
	g.RebuildEdges()
	res := NewRunner(root).VerifyAffected(g, []string{"core/x.go"}, VerifyOptions{Strict: true})
	if res.Status != "failed" || len(res.Pending) != 0 || res.TotalTargets != 2 || res.FailedTargets != 2 {
		t.Fatalf("want core and app both run and reported failing, nothing pending: %+v", res)
	}
}

// The re-run judges each failure: one that passes this time is dropped as flaky even when
// another failure in the same files persists (trpc: a ping timing test next to a memory test
// that fails on main blocked a correct push).
func TestRetryDropsFlakyFailureNextToPersistentOne(t *testing.T) {
	root := t.TempDir()
	_ = exec.Command("git", "-C", root, "init", "-q").Run()
	for _, f := range []string{"ws/src/link.test.ts", "ws/src/memory.test.ts"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0755)
		_ = os.WriteFile(filepath.Join(root, f), []byte("test"), 0644)
	}
	fake := `printf ' FAIL  |ws| src/memory.test.ts > memory > frees data\nAssertionError: leaked\n ❯ src/memory.test.ts:58:3\n'
if [ ! -f "$(git rev-parse --show-toplevel)/.seen" ]; then
  touch "$(git rev-parse --show-toplevel)/.seen"
  printf ' FAIL  |ws| src/link.test.ts > timeouts > ping\nAssertionError: expected "vi.fn()" to be called 1 times, but got 2 times\n ❯ src/link.test.ts:965:7\n'
fi
exit 1
`
	_ = os.WriteFile(filepath.Join(root, "vitest"), []byte(fake), 0755)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "ws", Path: "ws", Language: graph.LangTypeScript,
		TestCmd: `cd "$(git rev-parse --show-toplevel)" && sh vitest run --passWithNoTests ws`})
	res := NewRunner(root).VerifyPackages(g, []string{"ws"}, VerifyOptions{Strict: true})
	r := res.Results[0]
	if r.Status != "failed" || len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Message, "memory > frees data") {
		t.Fatalf("only the persistent failure should remain: %+v", r.Errors)
	}
}

// Files a check leaves in the repo (Playwright test-results/, coverage) are removed, so they
// cannot end up in the agent's commit; files that existed before are kept.
func TestVerifyRemovesFilesChecksCreate(t *testing.T) {
	root := t.TempDir()
	_ = exec.Command("git", "-C", root, "init", "-q").Run()
	_ = os.MkdirAll(filepath.Join(root, "app"), 0755)
	_ = os.WriteFile(filepath.Join(root, "app", "mine.txt"), []byte("agent's new file\n"), 0644)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangGeneric,
		TestCmd: "mkdir -p test-results && echo '{}' > test-results/.last-run.json"})
	res := NewRunner(root).VerifyPackages(g, []string{"app"}, VerifyOptions{Strict: true})
	if res.Status != "passed" {
		t.Fatalf("unexpected: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "test-results")); err == nil {
		t.Fatalf("check output must be removed")
	}
	if _, err := os.Stat(filepath.Join(root, "app", "mine.txt")); err != nil {
		t.Fatalf("pre-existing untracked file must be kept")
	}
}

// Example apps that merely depend on a change are skipped (verify_exclude, default examples/);
// an example changed directly is still verified.
func TestVerifyExcludeSkipsExampleDependents(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"core", "app", "examples/demo"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0755)
	}
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "core", Path: "core", Language: graph.LangGeneric, TestCmd: "true"})
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangGeneric, Dependencies: []string{"core"}, TestCmd: "true"})
	g.AddPackage(&graph.PackageNode{Name: "demo", Path: "examples/demo", Language: graph.LangGeneric, Dependencies: []string{"core"}, TestCmd: "true"})
	g.RebuildEdges()
	g.VerifyExclude = []string{"examples/"}
	r := NewRunner(root)
	res := r.VerifyAffected(g, []string{"core/x"}, VerifyOptions{Strict: true})
	if res.TotalTargets != 2 || res.Excluded != 1 {
		t.Fatalf("want core+app verified, demo excluded: %+v", res)
	}
	res = r.VerifyAffected(g, []string{"examples/demo/x"}, VerifyOptions{Strict: true})
	if res.TotalTargets != 1 || res.Excluded != 0 {
		t.Fatalf("a directly changed example must be verified: %+v", res)
	}
}

// In the agent's loop a dependent is not linted (a linter reads the package's own files, which
// the change did not touch); local CI (LintDependents) still lints it.
func TestDependentsAreLintedOnlyByLocalCI(t *testing.T) {
	root, marks := t.TempDir(), t.TempDir()
	for _, d := range []string{"core", "app"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0755)
	}
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "core", Path: "core", Language: graph.LangGeneric, LintCmd: "touch " + filepath.Join(marks, "core"), TestCmd: "true"})
	g.AddPackage(&graph.PackageNode{Name: "app", Path: "app", Language: graph.LangGeneric, Dependencies: []string{"core"}, LintCmd: "touch " + filepath.Join(marks, "app"), TestCmd: "true"})
	g.RebuildEdges()
	r := NewRunner(root)
	if res := r.VerifyAffected(g, []string{"core/x"}, VerifyOptions{Strict: true, NoCache: true}); res.Status != "passed" || res.TotalTargets != 2 {
		t.Fatalf("want both verified: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(marks, "core")); err != nil {
		t.Fatalf("the changed package must be linted")
	}
	if _, err := os.Stat(filepath.Join(marks, "app")); err == nil {
		t.Fatalf("a dependent must not be linted in the agent's loop")
	}
	r.VerifyAffected(g, []string{"core/x"}, VerifyOptions{Strict: true, NoCache: true, LintDependents: true})
	if _, err := os.Stat(filepath.Join(marks, "app")); err != nil {
		t.Fatalf("local CI must lint dependents")
	}
}

// A package failing its tests does not re-run the typecheck that passed on the same files.
func TestPassedCheckNotRerunWhileAnotherFails(t *testing.T) {
	root, marks := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "p"), 0755)
	count := filepath.Join(marks, "typecheck")
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "p", Path: "p", Language: graph.LangGeneric, TypecheckCmd: "echo x >> " + count, TestCmd: "exit 1"})
	g.RebuildEdges()
	r := NewRunner(root)
	for i := 0; i < 2; i++ {
		if res := r.VerifyAffected(g, []string{"p/x"}, VerifyOptions{Strict: true}); res.Status != "failed" {
			t.Fatalf("want the test failure: %+v", res)
		}
	}
	if data, _ := os.ReadFile(count); strings.Count(string(data), "x") != 1 {
		t.Fatalf("typecheck ran %d times, want 1", strings.Count(string(data), "x"))
	}
}

func TestTimingFailureJudgesTheFailureNotTheTestName(t *testing.T) {
	if timingFailure([]DiagnosticError{{Message: "[useQuery > refetchOnReconnect > works] AssertionError: expected 1 to be 2"}}) {
		t.Fatalf("an assertion failure in a test named after reconnects is not a timing flake")
	}
	if !timingFailure([]DiagnosticError{{Message: "[ws > keeps alive] Error: Test timed out in 5000ms"}}) {
		t.Fatalf("a timeout is a timing flake")
	}
}

func TestSlowestPackagesStartFirst(t *testing.T) {
	g := graph.New(t.TempDir())
	names := []string{"fast", "slow", "new", "mid"}
	order := slowestFirst(g.RootDir, g, names, map[string]float64{"fast": 1000, "slow": 30000, "mid": 9000})
	var got []string
	for _, i := range order {
		got = append(got, names[i])
	}
	if strings.Join(got, ",") != "new,slow,mid,fast" {
		t.Fatalf("order %v, want never-timed first, then slowest first", got)
	}
}
