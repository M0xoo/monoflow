package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestGitRepoForGuard(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "monoflow_guard_test_*")
	if err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	run("init")
	run("config", "user.name", "Test Agent")
	run("config", "user.email", "agent@monoflow.dev")

	testFile := filepath.Join(tmpDir, "tax.test.ts")
	_ = os.WriteFile(testFile, []byte("test('tax', () => {\n  expect(calculate(100)).toBe(120);\n});\n"), 0644)
	run("add", "tax.test.ts")
	run("commit", "-m", "init commit")

	return tmpDir
}

func TestCircuitBreaker(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_cb_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cb := NewCircuitBreaker(tmpDir, 3)

	// Attempt 1: not tripped
	st1, tripped1, err := cb.RecordFailure(tmpDir, "task-1", "services/billing")
	if err != nil || tripped1 || st1.Attempts != 1 {
		t.Fatalf("unexpected state 1: %+v, tripped: %v", st1, tripped1)
	}

	// Attempt 2: not tripped
	st2, tripped2, err := cb.RecordFailure(tmpDir, "task-1", "services/billing")
	if err != nil || tripped2 || st2.Attempts != 2 {
		t.Fatalf("unexpected state 2: %+v, tripped: %v", st2, tripped2)
	}

	// Attempt 3: TRIPPED!
	st3, tripped3, err := cb.RecordFailure(tmpDir, "task-1", "services/billing")
	if err != nil || !tripped3 || st3.Attempts != 3 || !st3.Tripped {
		t.Fatalf("expected circuit breaker to trip on attempt 3: %+v", st3)
	}
}

func TestCheckTestIntegrity(t *testing.T) {
	repoDir := setupTestGitRepoForGuard(t)
	defer os.RemoveAll(repoDir)

	// Weaken test by replacing real assertion with expect(true).toBe(true)
	testFile := filepath.Join(repoDir, "tax.test.ts")
	_ = os.WriteFile(testFile, []byte("test('tax', () => {\n  // expect(calculate(100)).toBe(120);\n  expect(true).toBe(true);\n});\n"), 0644)

	violations, err := CheckTestIntegrity(repoDir)
	if err != nil {
		t.Fatalf("CheckTestIntegrity failed: %v", err)
	}

	if len(violations) < 2 {
		t.Fatalf("expected at least 2 violations (deleted/commented assert + tautological assert), got %d: %+v", len(violations), violations)
	}
}

func TestScanSecrets(t *testing.T) {
	repoDir := setupTestGitRepoForGuard(t)
	defer os.RemoveAll(repoDir)

	// Leak AWS Key in code
	secretFile := filepath.Join(repoDir, "config.ts")
	_ = os.WriteFile(secretFile, []byte("const awsKey = 'AKIAIOSFODNN7EXAMPLE';\n"), 0644)

	cmd := exec.Command("git", "add", "config.ts")
	cmd.Dir = repoDir
	_ = cmd.Run()

	violations, err := ScanSecrets(repoDir)
	if err != nil {
		t.Fatalf("ScanSecrets failed: %v", err)
	}

	if len(violations) == 0 {
		t.Fatalf("expected secret leak violation for AWS key, got none")
	}
	if violations[0].Type != "secret_leak" {
		t.Errorf("unexpected violation type: %s", violations[0].Type)
	}
}

func TestFastAgentCommit(t *testing.T) {
	repoDir := setupTestGitRepoForGuard(t)
	defer os.RemoveAll(repoDir)

	// Valid edit
	validFile := filepath.Join(repoDir, "util.ts")
	_ = os.WriteFile(validFile, []byte("export const add = (a: number, b: number) => a + b;\n"), 0644)

	res, err := FastAgentCommit(repoDir, "feat: add utility")
	if err != nil {
		t.Fatalf("FastAgentCommit failed on valid edit: %v", err)
	}
	if res.Status != "committed" || res.CommitSHA == "" {
		t.Fatalf("expected committed status, got: %+v", res)
	}

	// Invalid edit (weakened test)
	testFile := filepath.Join(repoDir, "tax.test.ts")
	_ = os.WriteFile(testFile, []byte("test('tax', () => { expect(true).toBe(true); });\n"), 0644)

	_, err = FastAgentCommit(repoDir, "test: weakened test")
	if err == nil {
		t.Fatalf("expected FastAgentCommit to be blocked on weakened test, but it succeeded")
	}
}


func TestCheckTestIntegrity_AllowsAssertionRewrite(t *testing.T) {
	repoDir := setupTestGitRepoForGuard(t)
	defer os.RemoveAll(repoDir)

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	testFile := filepath.Join(repoDir, "money_test.go")
	_ = os.WriteFile(testFile, []byte("package m\n\nfunc TestP(t *testing.T) {\n\tif P(1000, 20) != 200 {\n\t\tt.Fatal(\"Percent broken\")\n\t}\n}\n"), 0644)
	run("add", "-A")
	run("commit", "-qm", "base")

	// Rewritten into a table test: one assertion removed, one stronger one added.
	_ = os.WriteFile(testFile, []byte("package m\n\nfunc TestP(t *testing.T) {\n\tfor _, c := range cases {\n\t\tif got := P(c.in, c.p); got != c.want {\n\t\t\tt.Fatalf(\"P(%d,%d)=%d want %d\", c.in, c.p, got, c.want)\n\t\t}\n\t}\n}\n"), 0644)
	if v, _ := CheckTestIntegrity(repoDir); len(v) != 0 {
		t.Fatalf("rewrite with equal assertions flagged: %+v", v)
	}

	// Pure deletion is still blocked.
	_ = os.WriteFile(testFile, []byte("package m\n\nfunc TestP(t *testing.T) {}\n"), 0644)
	if v, _ := CheckTestIntegrity(repoDir); len(v) == 0 || v[0].Type != "test_weakened" {
		t.Fatalf("expected test_weakened for deleted assertion, got %+v", v)
	}
}

func TestCheckTestIntegrity_ProseCommentNotAssertion(t *testing.T) {
	repoDir := setupTestGitRepoForGuard(t)
	defer os.RemoveAll(repoDir)
	testFile := filepath.Join(repoDir, "tax.test.ts")
	_ = os.WriteFile(testFile, []byte("test('tax', () => {\n  // one assertion removed, a stronger one added\n  expect(calculate(100)).toBe(120);\n});\n"), 0644)
	if v, _ := CheckTestIntegrity(repoDir); len(v) != 0 {
		t.Fatalf("prose comment flagged as commented-out assertion: %+v", v)
	}
	_ = os.WriteFile(testFile, []byte("test('tax', () => {\n  // expect(calculate(100)).toBe(120);\n});\n"), 0644)
	if v, _ := CheckTestIntegrity(repoDir); len(v) == 0 {
		t.Fatalf("commented-out expect() must still be flagged")
	}
}
