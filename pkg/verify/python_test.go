package verify

import (
	"os/exec"
	"strings"
	"testing"
)

// Ruff's default output (since 0.5) puts the location on the line after the finding.
func TestParseRuffFullOutput(t *testing.T) {
	out := "F401 [*] `os` imported but unused\n --> src/acme_core/__init__.py:1:8\n  |\n1 | import os\n  |        ^^\n  |\nhelp: Remove unused import: `os`\n\nFound 1 error.\n"
	errs := ParseDiagnostics(out)
	if len(errs) != 1 || errs[0].File != "src/acme_core/__init__.py" || errs[0].Line != 1 || errs[0].Col != 8 || errs[0].Code != "F401" || errs[0].Message != "`os` imported but unused" {
		t.Fatalf("got %+v", errs)
	}
}

func TestParsePytestFailures(t *testing.T) {
	out := `F.                                                                       [100%]
=================================== FAILURES ===================================
___________________________________ test_add ___________________________________

    def test_add():
>       assert add(1, 2) == 3
E       assert 4 == 3
E        +  where 4 = add(1, 2)

tests/test_core.py:5: AssertionError
=========================== short test summary info ============================
FAILED tests/test_core.py::test_add - assert 4 == 3
1 failed, 1 passed in 0.01s
`
	errs := ParseDiagnostics(out)
	if len(errs) != 1 || errs[0].File != "tests/test_core.py" || errs[0].Line != 5 || errs[0].Message != "[test_add] assert 4 == 3" {
		t.Fatalf("got %+v", errs)
	}
}

// A dependent whose import broke: point at the import, with the error, not at "error".
func TestParsePytestCollectionError(t *testing.T) {
	out := `
==================================== ERRORS ====================================
______________________ ERROR collecting tests/test_api.py ______________________
ImportError while importing test module '/r/packages/acme-api/tests/test_api.py'.
Traceback:
/usr/lib/python3.12/importlib/__init__.py:88: in import_module
    return _bootstrap._gcd_import(name[level:], package, level)
tests/test_api.py:1: in <module>
    from acme_api import total
src/acme_api/__init__.py:1: in <module>
    from acme_core import add
E   ImportError: cannot import name 'add' from 'acme_core'
=========================== short test summary info ============================
ERROR tests/test_api.py
!!!!!!!!!!!!!!!!!!!! Interrupted: 1 error during collection !!!!!!!!!!!!!!!!!!!!
`
	errs := ParseDiagnostics(out)
	if len(errs) != 1 || errs[0].File != "src/acme_api/__init__.py" || errs[0].Line != 1 || !strings.Contains(errs[0].Message, "ImportError: cannot import name 'add'") {
		t.Fatalf("got %+v", errs)
	}
}

// pytest exits 5 when a package has no tests (yet): nothing failed.
func TestPytestNoTestsIsPass(t *testing.T) {
	err := exec.Command("sh", "-c", "exit 5").Run()
	if noTestsIsPass("uv run pytest -q", []byte("\nno tests ran in 0.01s\n"), err) != nil {
		t.Error("pytest's 'no tests ran' must pass")
	}
	if noTestsIsPass("make check", []byte("no tests ran"), err) == nil {
		t.Error("exit 5 of another tool is a failure")
	}
}

// A check whose tool is missing never "pre-exists" on main: nothing ran on either side.
func TestMissingToolIsEnvironmentFailure(t *testing.T) {
	for _, msg := range []string{"sh: ruff: command not found", "sh: 1: pytest: not found", "error: Failed to spawn: `pytest`\n  Caused by: No such file or directory (os error 2)", "/usr/bin/python3: No module named pytest"} {
		if !EnvironmentFailure([]DiagnosticError{{Code: "execution_failure", Message: msg}}) {
			t.Errorf("not an environment failure: %q", msg)
		}
	}
	if EnvironmentFailure([]DiagnosticError{{Code: "test-failure", Message: "[test_x] assert 1 == 2"}}) {
		t.Error("a failing test is not an environment failure")
	}
}
