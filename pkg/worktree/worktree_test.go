package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "monoflow_wt_test_*")
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

	// Dummy files and dummy node_modules
	_ = os.WriteFile(filepath.Join(tmpDir, "index.ts"), []byte("console.log('root');\n"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg.json"), []byte("{}"), 0644)

	run("add", "index.ts")
	run("commit", "-m", "init commit")

	return tmpDir
}

func TestWorktreeCreateListSyncFinish(t *testing.T) {
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)

	mgr := NewManager(repoDir)

	// 1. Create worktree for Agent Alpha
	infoA, err := mgr.Create("agent-alpha")
	if err != nil {
		t.Fatalf("failed to create worktree alpha: %v", err)
	}
	if infoA.AgentID != "agent-alpha" {
		t.Errorf("unexpected agent ID: %s", infoA.AgentID)
	}

	// Verify node_modules was symlinked without copying!
	symlinkPath := filepath.Join(infoA.Path, "node_modules")
	fi, err := os.Lstat(symlinkPath)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink for node_modules in worktree")
	}

	// 2. Create worktree for Agent Beta
	infoB, err := mgr.Create("agent-beta")
	if err != nil {
		t.Fatalf("failed to create worktree beta: %v", err)
	}

	// 3. List active worktrees
	list, err := mgr.List()
	if err != nil {
		t.Fatalf("failed to list worktrees: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(list))
	}

	// 4. Agent Alpha modifies index.ts
	alphaFile := filepath.Join(infoA.Path, "index.ts")
	err = os.WriteFile(alphaFile, []byte("console.log('alpha edit');\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// 5. Sync from Agent Alpha to Agent Beta
	synced, err := mgr.Sync("agent-beta", "agent-alpha")
	if err != nil {
		t.Fatalf("failed to sync worktrees: %v", err)
	}
	if len(synced) != 1 || synced[0] != "index.ts" {
		t.Errorf("unexpected synced files: %v", synced)
	}

	// Verify Agent Beta has the updated content
	betaFile := filepath.Join(infoB.Path, "index.ts")
	betaContent, _ := os.ReadFile(betaFile)
	if string(betaContent) != "console.log('alpha edit');\n" {
		t.Fatalf("expected beta to have synced content, got: %s", string(betaContent))
	}

	// 6. Finish worktree
	err = mgr.Finish("agent-alpha")
	if err != nil {
		t.Fatalf("failed to finish worktree alpha: %v", err)
	}

	remaining, _ := mgr.List()
	if len(remaining) != 1 || remaining[0].AgentID != "agent-beta" {
		t.Fatalf("expected 1 remaining worktree (beta), got %d", len(remaining))
	}
}
