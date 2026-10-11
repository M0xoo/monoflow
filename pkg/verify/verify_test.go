package verify

import (
	"os"
	"os/exec"
	"path/filepath"
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

// While a changed package fails, its dependents are not run (fast fix loop); once it passes
// they are, so a pass still covers every dependent.
func TestVerifyAffectedChecksChangedPackagesFirst(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "core"), 0755)
	_ = os.MkdirAll(filepath.Join(root, "app"), 0755)
	_ = os.WriteFile(filepath.Join(root, "core", "ok"), []byte("no\n"), 0644)
	g := graph.New(root)
	g.AddPackage(&graph.PackageNode{Name: "core", Path: "core", Language: graph.LangGeneric, TestCmd: `grep -qx yes ok || { echo "core_test.go:1: not ok"; exit 1; }`})
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
