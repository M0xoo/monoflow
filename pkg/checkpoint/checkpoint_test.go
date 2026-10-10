package checkpoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestGitRepo(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "monoflow_chk_test_*")
	if err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v, out: %s", args, err, string(out))
		}
	}

	run("init")
	run("config", "user.name", "Test Agent")
	run("config", "user.email", "agent@monoflow.dev")

	// Initial commit
	initFile := filepath.Join(tmpDir, "hello.txt")
	os.WriteFile(initFile, []byte("initial state\n"), 0644)
	run("add", "hello.txt")
	run("commit", "-m", "initial commit")

	return tmpDir
}

func TestCheckpointSaveAndRevert(t *testing.T) {
	repoDir := setupTestGitRepo(t)
	defer os.RemoveAll(repoDir)

	mgr := NewManager(repoDir)
	filePath := filepath.Join(repoDir, "hello.txt")

	// 1. First edit: Valid feature state
	err := os.WriteFile(filePath, []byte("feature state 1\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	chk1, err := mgr.Save("good feature 1")
	if err != nil {
		t.Fatalf("failed to save checkpoint: %v", err)
	}
	if chk1.ID != "chk_001" {
		t.Errorf("expected chk_001, got %s", chk1.ID)
	}

	// 2. Second edit: Broken / experimental state
	err = os.WriteFile(filePath, []byte("broken experimental syntax\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Verify file has broken content
	content, _ := os.ReadFile(filePath)
	if string(content) != "broken experimental syntax\n" {
		t.Fatalf("unexpected content before revert: %s", string(content))
	}

	// 3. Revert to chk_001
	err = mgr.Revert("chk_001")
	if err != nil {
		t.Fatalf("failed to revert checkpoint: %v", err)
	}

	// 4. Verify file is restored to feature state 1!
	revertedContent, _ := os.ReadFile(filePath)
	if string(revertedContent) != "feature state 1\n" {
		t.Fatalf("expected 'feature state 1\\n', got '%s'", string(revertedContent))
	}
}
