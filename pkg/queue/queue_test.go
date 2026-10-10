package queue

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestQueueSpeculativeReceipt(t *testing.T) {
	tmpDir := t.TempDir()
	runCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}
	runCmd("init", "-b", "main")
	runCmd("config", "user.name", "Test Agent")
	runCmd("config", "user.email", "agent@test.local")
	_ = os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# test"), 0644)
	runCmd("add", "README.md")
	runCmd("commit", "-m", "init")

	qm := NewQueueManager(tmpDir)

	changed := []string{"services/billing/billing.go"}
	receipt, err := qm.SubmitSpeculative("task-merge-01", nil, changed, SpeculativeOptions{
		TargetBranch:    "HEAD",
		SkipQuarantined: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receipt.Status != "speculatively_verified" {
		t.Errorf("expected speculatively_verified, got %s", receipt.Status)
	}

	if receipt.Signature == "" {
		t.Errorf("expected signature, got empty")
	}

	// Verify CI Gate
	eligible, rec, reason := qm.VerifyCIGate(receipt.HeadCommitSHA)
	if !eligible {
		t.Fatalf("expected fastPathEligible=true, reason: %s", reason)
	}

	if rec.TaskID != "task-merge-01" {
		t.Errorf("expected task-merge-01, got %s", rec.TaskID)
	}
}

func TestVerifyCIGateTampering(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow-queue-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	qm := NewQueueManager(tmpDir)

	receipt := &QueueReceipt{
		TaskID:              "tamper-task",
		HeadCommitSHA:       "12345678abcdef",
		SpeculativeMergeSHA: "abcdef123456",
		DurationMs:          10.5,
		Signature:           "forged_signature",
	}

	_ = qm.SaveReceipt(receipt)

	eligible, _, reason := qm.VerifyCIGate("12345678abcdef")
	if eligible {
		t.Fatalf("expected tampering rejection, but got eligible=true")
	}

	if reason == "" {
		t.Errorf("expected rejection reason")
	}
}
