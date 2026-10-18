package main

import (
	"github.com/antigravity/monoflow/pkg/verify"
	"strconv"
	"strings"
	"testing"
)

func TestFindGroupsAndLeavesOutGenerated(t *testing.T) {
	out := findText("BAD", strings.Join([]string{
		"src/codes.ts:20:  BAD: -32600,",
		"src/codes.test.ts:5:expect(BAD)",
		"src/codes.test.ts:9:expect(BAD)",
		"docs/errors.md:3:BAD means",
		"CHANGELOG.md:100:BAD renamed",
		"docs/reference/api.md:7:BAD",
	}, "\n"))
	for _, want := range []string{"BAD: 6 matches; files: code 1, tests 1, docs 1 (2 in changelogs/generated/build output left out)", "  src/codes.ts:20  BAD: -32600,", "  src/codes.test.ts:5  expect(BAD)", "  src/codes.test.ts:9  expect(BAD)", "  docs/errors.md:3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CHANGELOG") {
		t.Fatalf("changelog must be left out:\n%s", out)
	}
}

// Many matches: test files go back to line numbers only (the output stays compact).
func TestFindManyTestMatchesAsLineNumbers(t *testing.T) {
	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, "src/codes.test.ts:"+strconv.Itoa(i)+":expect(BAD)")
	}
	if out := findText("BAD", strings.Join(lines, "\n")); !strings.Contains(out, "  src/codes.test.ts:1,2,3") {
		t.Fatalf("want line numbers for many test matches:\n%s", out)
	}
}

func TestEnvironmentFailureIsNotARealResult(t *testing.T) {
	broken := verify.SuiteResult{Results: []verify.TargetResult{{Status: "failed", Errors: []verify.DiagnosticError{{Code: "execution_failure", Message: "sh: pnpm: command not found"}}}}}
	real := verify.SuiteResult{Results: []verify.TargetResult{{Status: "failed", Errors: []verify.DiagnosticError{{Code: "test-failure", Message: "[a > b] expected 1 to be 2"}}}}}
	if !environmentFailure(broken) || environmentFailure(real) {
		t.Fatalf("a missing tool is an environment failure; a failing test is not")
	}
}

func TestFindKindTestsByName(t *testing.T) {
	for f, want := range map[string]string{"packages/api-client/test.js": "tests", "lib/test_money.py": "tests", "lib/money_test.py": "tests",
		"spec/x_spec.rb": "tests", "src/contest.js": "code", "src/testing.go": "code"} {
		if got := findKind(f); got != want {
			t.Errorf("findKind(%s) = %s, want %s", f, got, want)
		}
	}
}
