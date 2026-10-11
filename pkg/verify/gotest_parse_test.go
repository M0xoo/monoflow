package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDiagnosticsGoTestFailure(t *testing.T) {
	out := "--- FAIL: TestLocalFixture (0.00s)\n    local_test.go:11: open local.txt: no such file or directory\nFAIL\nFAIL\texample.com/wake/pkg/a\t0.198s\nFAIL\n"
	errs := ParseDiagnostics(out)
	if len(errs) != 1 {
		t.Fatalf("want 1 diagnostic, got %d: %+v", len(errs), errs)
	}
	e := errs[0]
	if e.File != "local_test.go" || e.Line != 11 || e.Code != "test-failure" {
		t.Fatalf("bad location: %+v", e)
	}
	if e.Message != "[TestLocalFixture] open local.txt: no such file or directory" {
		t.Fatalf("bad message: %q", e.Message)
	}
}

// testify prints the location alone, then the message and a diff on following lines.
func TestParseDiagnosticsTestifyFailure(t *testing.T) {
	out := "--- FAIL: TestExporterExportSpan (0.00s)\n" +
		"    trace_test.go:110: \n" +
		"        \tError Trace:\t/abs/exporters/stdout/stdouttrace/trace_test.go:110\n" +
		"        \tError:      \tNot equal: \n" +
		"        \t            \texpected: \"{\\n\\t\\\"Name\\\": \\\"/foo\\\"\"\n" +
		"        \t            \tactual  : \"{\\n\\t\\\"Name\\\": \\\"/foo\\\"\"\n" +
		"        \t            \t\n" +
		"        \t            \tDiff:\n" +
		"        \t            \t--- Expected\n" +
		"        \t            \t+++ Actual\n" +
		"        \t            \t@@ -67,3 +67,3 @@\n" +
		"        \t            \t \t\"Status\": {\n" +
		"        \t            \t-\t\t\"Code\": \"Error\",\n" +
		"        \t            \t+\t\t\"Code\": \"ERROR\",\n" +
		"        \tTest:       \tTestExporterExportSpan\n" +
		"FAIL\n" +
		"FAIL\tgo.opentelemetry.io/otel/exporters/stdout/stdouttrace\t0.364s\n"
	errs := ParseDiagnostics(out)
	if len(errs) != 1 {
		t.Fatalf("want 1 diagnostic, got %d: %+v", len(errs), errs)
	}
	e := errs[0]
	if e.File != "trace_test.go" || e.Line != 110 {
		t.Fatalf("bad location: %+v", e)
	}
	for _, want := range []string{"[TestExporterExportSpan] Not equal:", "-\t\t\"Code\": \"Error\",", "+\t\t\"Code\": \"ERROR\","} {
		if !strings.Contains(e.Message, want) {
			t.Fatalf("message %q lacks %q", e.Message, want)
		}
	}
}

// go test names only the file; the "FAIL <import path>" line places it in the module.
func TestResolveGoTestFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module go.opentelemetry.io/otel\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out := "--- FAIL: TestCodeString (0.00s)\n    codes_test.go:25: bad\nFAIL\nFAIL\tgo.opentelemetry.io/otel/codes\t0.2s\n" +
		"--- FAIL: TestRoot (0.00s)\n    root_test.go:9: bad\nFAIL\nFAIL\tgo.opentelemetry.io/otel\t0.1s\n"
	errs := ParseDiagnostics(out)
	ResolveGoTestFiles(errs, dir)
	if len(errs) != 2 || errs[0].File != "codes/codes_test.go" || errs[1].File != "root_test.go" {
		t.Fatalf("bad files: %+v", errs)
	}
}

func TestParseDiagnosticsVitestFailure(t *testing.T) {
	out := "⎯⎯⎯⎯⎯⎯⎯ Failed Tests 1 ⎯⎯⎯⎯⎯⎯⎯\n\n" +
		" FAIL  |@trpc/server| src/unstable-core-do-not-import/procedureBuilder.test.ts > concat() > basic\n" +
		"AssertionError: expected 'UNPROCESSABLE_CONTENT' to be 'BAD_REQUEST' // Object.is equality\n\n" +
		"Expected: \"BAD_REQUEST\"\nReceived: \"UNPROCESSABLE_CONTENT\"\n\n" +
		" ❯ src/unstable-core-do-not-import/procedureBuilder.test.ts:285:22\n" +
		"    283|       TRPCError,\n    285|     expect(err.code).toBe('BAD_REQUEST');\n" +
		"⎯⎯⎯⎯⎯⎯⎯⎯⎯⎯[1/1]⎯\n\n Test Files  1 failed | 18 passed (19)\n"
	errs := ParseDiagnostics(out)
	if len(errs) != 1 {
		t.Fatalf("want 1, got %d: %+v", len(errs), errs)
	}
	e := errs[0]
	if e.File != "src/unstable-core-do-not-import/procedureBuilder.test.ts" || e.Line != 285 || e.Col != 22 {
		t.Fatalf("bad location: %+v", e)
	}
	want := `[concat() > basic] AssertionError: expected 'UNPROCESSABLE_CONTENT' to be 'BAD_REQUEST' // Object.is equality; Expected: "BAD_REQUEST"; Received: "UNPROCESSABLE_CONTENT"`
	if e.Message != want {
		t.Fatalf("message:\n got %q\nwant %q", e.Message, want)
	}
}

func TestParseDiagnosticsJestFailure(t *testing.T) {
	out := "  ● math › adds\n\n    expect(received).toBe(expected) // Object.is equality\n\n    Expected: 3\n    Received: 4\n\n" +
		"      at Object.<anonymous> (src/math.test.ts:7:19)\n"
	errs := ParseDiagnostics(out)
	if len(errs) != 1 || errs[0].File != "src/math.test.ts" || errs[0].Line != 7 ||
		errs[0].Message != "[math › adds] expect(received).toBe(expected) // Object.is equality; Expected: 3; Received: 4" {
		t.Fatalf("got %+v", errs)
	}
}
