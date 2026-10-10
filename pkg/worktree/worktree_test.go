package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	_ = os.WriteFile(filepath.Join(tmpDir, "node_modules", "pkg.json"), []byte(`{"name":"original"}`), 0644)

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

func TestWorktreeIsolateDeps(t *testing.T) {
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)

	mgr := NewManager(repoDir)

	// 1. Create with IsolatedDeps = true
	info, err := mgr.CreateWithOpts("agent-upgrade", CreateOptions{IsolatedDeps: true})
	if err != nil {
		t.Fatalf("failed to create isolated worktree: %v", err)
	}

	if !info.Isolated {
		t.Errorf("expected info.Isolated to be true")
	}

	// Verify node_modules is a real cloned directory, NOT a symlink
	nodeModulesPath := filepath.Join(info.Path, "node_modules")
	fi, err := os.Lstat(nodeModulesPath)
	if err != nil {
		t.Fatalf("node_modules not found in isolated worktree: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected real directory, but node_modules is still a symlink")
	}

	// 2. Modify node_modules in the worktree
	worktreePkgFile := filepath.Join(nodeModulesPath, "pkg.json")
	err = os.WriteFile(worktreePkgFile, []byte(`{"name":"upgraded-v2"}`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Verify root node_modules was NOT modified (true isolation)
	rootPkgFile := filepath.Join(repoDir, "node_modules", "pkg.json")
	rootData, _ := os.ReadFile(rootPkgFile)
	if string(rootData) != `{"name":"original"}` {
		t.Fatalf("root node_modules was corrupted by worktree! got: %s", string(rootData))
	}

	// 4. Test Isolate on an existing shared worktree
	sharedInfo, err := mgr.Create("agent-shared")
	if err != nil {
		t.Fatal(err)
	}

	isoRes, err := mgr.Isolate("agent-shared")
	if err != nil {
		t.Fatalf("Isolate failed: %v", err)
	}
	if isoRes.Status != "isolated" {
		t.Errorf("expected isolated status, got %s", isoRes.Status)
	}

	// Verify shared worktree's node_modules is now also an independent directory
	sharedNM := filepath.Join(sharedInfo.Path, "node_modules")
	fiShared, _ := os.Lstat(sharedNM)
	if fiShared.Mode()&os.ModeSymlink != 0 {
		t.Errorf("expected promoted node_modules to be a real directory, not symlink")
	}
}

func TestWorktreeConcurrentSameFileASTMerge(t *testing.T) {
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)

	mgr := NewManager(repoDir)

	// Agent 1
	info1, err := mgr.Create("agent-1")
	if err != nil {
		t.Fatal(err)
	}

	// Agent 2
	info2, err := mgr.Create("agent-2")
	if err != nil {
		t.Fatal(err)
	}

	// Agent 1 adds alphaFn to index.ts
	f1 := filepath.Join(info1.Path, "index.ts")
	_ = os.WriteFile(f1, []byte("console.log('root');\n\nexport function alphaFn() { return 1; }\n"), 0644)

	// Agent 2 adds betaFn to index.ts
	f2 := filepath.Join(info2.Path, "index.ts")
	_ = os.WriteFile(f2, []byte("console.log('root');\n\nexport function betaFn() { return 2; }\n"), 0644)

	// Sync from agent-1 into agent-2
	synced, err := mgr.Sync("agent-2", "agent-1")
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if len(synced) == 0 {
		t.Fatalf("expected synced files, got none")
	}

	content2, _ := os.ReadFile(f2)
	str2 := string(content2)

	if !strings.Contains(str2, "alphaFn") || !strings.Contains(str2, "betaFn") {
		t.Fatalf("expected both alphaFn and betaFn synthesized, got:\n%s", str2)
	}
}

func TestWorktreeHeartbeatAndGC(t *testing.T) {
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)

	mgr := NewManager(repoDir)

	_, err := mgr.Create("stale-agent")
	if err != nil {
		t.Fatal(err)
	}

	_ = mgr.Touch("stale-agent")

	// Prune with -1 duration to simulate expired lease
	pruned, err := mgr.PruneStale(-1 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned) == 0 {
		t.Fatalf("expected stale worktree to be pruned")
	}

	list, _ := mgr.List()
	if len(list) != 0 {
		t.Fatalf("expected 0 active worktrees after GC, got %d", len(list))
	}
}
