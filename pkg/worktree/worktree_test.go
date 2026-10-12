package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/cache"
	initialize "github.com/antigravity/monoflow/pkg/init"
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

	run("init", "-b", "main")
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

	// node_modules is an overlay: entries link to the root install, nothing is copied.
	entry := filepath.Join(infoA.Path, "node_modules", "pkg.json")
	if fi, err := os.Lstat(entry); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected node_modules entries linked to the root install")
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

func TestWorktreeSparsePackageSubset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_wt_sparse_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	run("init", "-b", "main")
	run("config", "user.name", "Test Agent")
	run("config", "user.email", "agent@monoflow.dev")

	_ = os.MkdirAll(filepath.Join(tmpDir, "pkgA"), 0755)
	_ = os.MkdirAll(filepath.Join(tmpDir, "pkgB"), 0755)
	_ = os.MkdirAll(filepath.Join(tmpDir, ".agents"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "pkgA", "a.go"), []byte("package pkgA\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "pkgB", "b.go"), []byte("package pkgB\n"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "root.go"), []byte("package root\n"), 0644)

	configJSON := `{
  "version": "1.0",
  "packages": [
    {"name": "pkgA", "path": "pkgA", "language": "go", "dependencies": []},
    {"name": "pkgB", "path": "pkgB", "language": "go", "dependencies": []}
  ]
}`
	_ = os.WriteFile(filepath.Join(tmpDir, ".agents", "monoflow.config.json"), []byte(configJSON), 0644)

	run("add", ".")
	run("commit", "-m", "init multi-package monorepo")

	mgr := NewManager(tmpDir)

	// Create worktree for worker-a requesting ONLY pkgA
	info, err := mgr.CreateWithOpts("worker-a", CreateOptions{
		Packages:    []string{"pkgA"},
		IncludeDeps: true,
	})
	if err != nil {
		t.Fatalf("failed to create sparse worktree: %v", err)
	}

	if !info.IsSparse {
		t.Errorf("expected info.IsSparse to be true")
	}
	if len(info.Packages) != 1 || info.Packages[0] != "pkgA" {
		t.Errorf("expected pkgA in info.Packages, got: %v", info.Packages)
	}

	// Verify pkgA and root.go exist in the worktree
	if _, err := os.Stat(filepath.Join(info.Path, "pkgA", "a.go")); err != nil {
		t.Errorf("expected pkgA to exist in worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "root.go")); err != nil {
		t.Errorf("expected root.go to exist in worktree: %v", err)
	}

	// Verify pkgB is OMITTED from the worktree
	if _, err := os.Stat(filepath.Join(info.Path, "pkgB")); !os.IsNotExist(err) {
		t.Errorf("expected pkgB to be omitted from sparse worktree, but it exists")
	}

	// 1. Add pkgB to the worktree
	addInfo, err := mgr.AddPackage("worker-a", []string{"pkgB"}, "")
	if err != nil {
		t.Fatalf("AddPackage failed: %v", err)
	}
	if len(addInfo.Packages) != 2 {
		t.Errorf("expected 2 packages after AddPackage, got: %v", addInfo.Packages)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "pkgB", "b.go")); err != nil {
		t.Errorf("expected pkgB to exist in worktree after AddPackage: %v", err)
	}

	// 2. Remove pkgA from the worktree
	remInfo, err := mgr.RemovePackage("worker-a", []string{"pkgA"}, false)
	if err != nil {
		t.Fatalf("RemovePackage failed: %v", err)
	}
	if len(remInfo.Packages) != 1 || remInfo.Packages[0] != "pkgB" {
		t.Errorf("expected only pkgB in worktree after RemovePackage, got: %v", remInfo.Packages)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "pkgA")); !os.IsNotExist(err) {
		t.Errorf("expected pkgA to be pruned from worktree after RemovePackage")
	}

	// 3. Focus worktree back on pkgA
	focusInfo, err := mgr.Focus("worker-a", "pkgA", "")
	if err != nil {
		t.Fatalf("Focus failed: %v", err)
	}
	if len(focusInfo.Packages) != 1 || focusInfo.Packages[0] != "pkgA" {
		t.Errorf("expected only pkgA after Focus, got: %v", focusInfo.Packages)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "pkgA", "a.go")); err != nil {
		t.Errorf("expected pkgA to exist after Focus: %v", err)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "pkgB")); !os.IsNotExist(err) {
		t.Errorf("expected pkgB to be omitted after Focus")
	}
}


func TestWorktreeCreateProvisionsBinShieldAndSharedCache(t *testing.T) {
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)

	// Root repo carries a monoflow binary and is shielded.
	_ = os.MkdirAll(filepath.Join(repoDir, "bin"), 0755)
	rootBin := filepath.Join(repoDir, "bin", "monoflow")
	_ = os.WriteFile(rootBin, []byte("#!/bin/sh\necho monoflow\n"), 0755)
	if _, err := initialize.InstallShieldHooks(repoDir); err != nil {
		t.Fatalf("failed to install shield hooks: %v", err)
	}

	mgr := NewManager(repoDir)
	info, err := mgr.Create("agent-beta")
	if err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// 1. ./bin/monoflow resolves to the root binary and is not reported as a change.
	wtBin := filepath.Join(info.Path, "bin", "monoflow")
	target, err := os.Readlink(wtBin)
	if err != nil || target != rootBin {
		t.Fatalf("expected %s -> %s, got %q (err=%v)", wtBin, rootBin, target, err)
	}
	if info.MonoflowBin != wtBin {
		t.Errorf("expected MonoflowBin %s, got %s", wtBin, info.MonoflowBin)
	}
	status := exec.Command("git", "status", "--porcelain", "--", "bin")
	status.Dir = info.Path
	if out, _ := status.CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("expected bin/monoflow to be git-ignored in worktree, got status: %s", out)
	}

	// 2. Raw git commit inside the worktree is blocked by the inherited shield.
	if !info.ShieldHooks {
		t.Errorf("expected worktree to report shield hooks active")
	}
	_ = os.WriteFile(filepath.Join(info.Path, "index.ts"), []byte("console.log('beta');\n"), 0644)
	commit := exec.Command("git", "commit", "-am", "raw commit")
	commit.Dir = info.Path
	commit.Env = append(os.Environ(), "MONOFLOW_INTERNAL=0")
	if out, err := commit.CombinedOutput(); err == nil || !strings.Contains(string(out), "MONOFLOW SHIELD") {
		t.Fatalf("expected raw git commit in worktree to be blocked by shield, err=%v out=%s", err, out)
	}

	// 3. Package cache is shared with the root repo.
	cacheLink := filepath.Join(info.Path, ".agents", "cache")
	if target, err := os.Readlink(cacheLink); err != nil || target != filepath.Join(repoDir, ".agents", "cache") {
		t.Fatalf("expected worktree cache linked to root cache, got %q (err=%v)", target, err)
	}
	gotDir, _ := filepath.EvalSymlinks(filepath.Dir(cache.SharedCacheDir(info.Path)))
	wantDir, _ := filepath.EvalSymlinks(filepath.Join(repoDir, ".agents"))
	if gotDir != wantDir {
		t.Fatalf("expected SharedCacheDir in a worktree to resolve to the root cache, got %s want %s", gotDir, wantDir)
	}
}

// A worktree keeps the config its branch commits; the root checkout's copy (another branch, or
// local edits) is used only when the branch has none.
func TestWorktreeKeepsBranchConfig(t *testing.T) {
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	cfg := filepath.Join(repoDir, ".agents", "monoflow.config.json")
	_ = os.MkdirAll(filepath.Dir(cfg), 0755)
	_ = os.WriteFile(cfg, []byte(`{"committed": true}`), 0644)
	run("add", ".agents/monoflow.config.json")
	run("commit", "-m", "config")
	_ = os.WriteFile(cfg, []byte(`{"root_local": true}`), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, ".agents", "task.json"), []byte(`{"task_id": "root"}`), 0644)

	info, err := NewManager(repoDir).Create("agent-cfg")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(info.Path, ".agents", "monoflow.config.json")); string(got) != `{"committed": true}` {
		t.Fatalf("worktree must keep its branch's config, got %s", got)
	}
	if got, _ := os.ReadFile(filepath.Join(info.Path, ".agents", "task.json")); string(got) != `{"task_id": "root"}` {
		t.Fatalf("a file the branch does not have is still provided from the root, got %s", got)
	}
}

// A new worktree starts from origin's base (freshly fetched), not the local one: a local commit
// origin lacks is left out and reported; FromLocal starts from the local base instead.
func TestWorktreeStartsFromOriginBase(t *testing.T) {
	remote := t.TempDir()
	repoDir := setupTestRepo(t)
	defer os.RemoveAll(repoDir)
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(remote, "init", "-q", "--bare", "-b", "main")
	git(repoDir, "remote", "add", "origin", remote)
	git(repoDir, "push", "-q", "origin", "main")
	_ = os.WriteFile(filepath.Join(repoDir, "local.txt"), []byte("local only\n"), 0644)
	git(repoDir, "add", "local.txt")
	git(repoDir, "commit", "-qm", "local only")

	mgr := NewManager(repoDir)
	info, err := mgr.CreateWithOpts("from-origin", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.BaseRef != "origin/main" || info.LocalOnlyCommits != 1 {
		t.Fatalf("base %q, local-only %d", info.BaseRef, info.LocalOnlyCommits)
	}
	if _, err := os.Stat(filepath.Join(info.Path, "local.txt")); err == nil {
		t.Fatal("the local-only commit must not be in a worktree cut from origin/main")
	}
	local, err := mgr.CreateWithOpts("from-local", CreateOptions{FromLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(local.Path, "local.txt")); err != nil || local.BaseRef != "main" {
		t.Fatalf("FromLocal must start from the local main (base %q)", local.BaseRef)
	}
}
