package verify

import (
	"os"
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
