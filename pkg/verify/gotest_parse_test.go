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
