package verify

import "testing"

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
