package pr

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/review"
	"github.com/antigravity/monoflow/pkg/subset"
)

func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	run("init", "-b", "main")
	run("config", "user.name", "Test Agent")
	run("config", "user.email", "agent@monoflow.test")

	// Create 2 packages: core, api
	_ = os.MkdirAll(filepath.Join(dir, "packages", "core"), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "packages", "api"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "root", "workspaces": ["packages/*"]}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "packages", "core", "package.json"), []byte(`{"name": "@monoflow/core", "scripts": {"test": "node -c index.js"}}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "packages", "core", "index.js"), []byte(`module.exports = { core: true };`), 0644)

	_ = os.WriteFile(filepath.Join(dir, "packages", "api", "package.json"), []byte(`{"name": "@monoflow/api", "scripts": {"test": "node -c index.js"}, "dependencies": {"@monoflow/core": "*"}}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "packages", "api", "index.js"), []byte(`const core = require('./index.js'); module.exports = { api: true };`), 0644)

	run("add", "-A")
	run("commit", "-m", "initial commit on main")

	return dir
}

func TestPRLifecycle(t *testing.T) {
	repo := setupTestGitRepo(t)
	mgr := NewManager(repo)

	// 1. Create a feature branch with a change
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	runGit("checkout", "-b", "feat/billing-update")
	_ = os.WriteFile(filepath.Join(repo, "packages", "core", "billing.js"), []byte(`module.exports = { bill: () => 100 };`), 0644)
	runGit("add", "-A")
	runGit("commit", "-m", "feat: add billing module")

	// 2. Test CreatePR
	prMeta, err := mgr.Create(CreateOptions{
		Title:      "feat(core): add billing module",
		Body:       "Introduces high-speed billing primitives",
		HeadBranch: "feat/billing-update",
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("Create PR failed: %v", err)
	}

	if prMeta.ID == "" {
		t.Fatalf("Expected non-empty PR ID")
	}
	if prMeta.Risk == nil {
		t.Fatalf("Expected risk assessment attached to PR")
	}
	if prMeta.State != StateOpen {
		t.Fatalf("Expected PR state OPEN, got %s", prMeta.State)
	}

	// 3. Test ViewPR
	viewed, err := mgr.View(prMeta.ID)
	if err != nil {
		t.Fatalf("View PR failed: %v", err)
	}
	if viewed.Title != "feat(core): add billing module" {
		t.Fatalf("Expected PR title match, got %s", viewed.Title)
	}

	// 4. Test ListPRs
	list, err := mgr.List(ListOptions{State: "open"})
	if err != nil {
		t.Fatalf("List PRs failed: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("Expected at least 1 open PR in list")
	}

	// 5. Test ReviewPR (Approve)
	revRes, err := mgr.Review(prMeta.ID, ReviewOptions{
		Approve: true,
		Comment: "LGTM! Machine verified.",
	})
	if err != nil {
		t.Fatalf("Review PR failed: %v", err)
	}
	if revRes.ReviewDecision != "APPROVED" {
		t.Fatalf("Expected review decision APPROVED, got %s", revRes.ReviewDecision)
	}

	// 6. Test TestPR (Delta verification)
	testRes, err := mgr.Test(prMeta.ID, TestOptions{
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("Test PR failed: %v", err)
	}
	if testRes.Status != "passed" {
		t.Fatalf("Expected test status passed, got %s", testRes.Status)
	}

	// 7. Test LocalPR (worktree mode)
	locRes, err := mgr.Local(prMeta.ID, LocalOptions{
		WorktreeID:  "agent-test-wt",
		DepStrategy: subset.DepStrategyContracts,
	})
	if err != nil {
		t.Fatalf("Local PR worktree mode failed: %v", err)
	}
	if locRes.Mode != "worktree" {
		t.Fatalf("Expected mode 'worktree', got %s", locRes.Mode)
	}
	if locRes.Status != "worktree_created" {
		t.Fatalf("Expected status 'worktree_created', got %s", locRes.Status)
	}
	if _, err := os.Stat(locRes.WorkingDir); os.IsNotExist(err) {
		t.Fatalf("Expected worktree directory %s to exist", locRes.WorkingDir)
	}

	// 7b. Test LocalPR (workspace in-place projection)
	locWsRes, err := mgr.Local(prMeta.ID, LocalOptions{
		DepStrategy: subset.DepStrategyContracts,
	})
	if err != nil {
		t.Fatalf("Local PR workspace mode failed: %v", err)
	}
	if locWsRes.Mode != "workspace" {
		t.Fatalf("Expected mode 'workspace', got %s", locWsRes.Mode)
	}
	if locWsRes.Status != "projected" {
		t.Fatalf("Expected status 'projected', got %s", locWsRes.Status)
	}

	// 8. Test MergePR (Local squash merge)
	mergeRes, err := mgr.Merge(prMeta.ID, MergeOptions{
		Strategy:     "squash",
		DeleteBranch: true,
	})
	if err != nil {
		t.Fatalf("Merge PR failed: %v", err)
	}
	if mergeRes.Status != "merged" {
		t.Fatalf("Expected status 'merged', got %s", mergeRes.Status)
	}

	// Verify state updated in registry
	updatedView, err := mgr.View(prMeta.ID)
	if err != nil {
		t.Fatalf("View merged PR failed: %v", err)
	}
	if updatedView.State != StateMerged {
		t.Fatalf("Expected state MERGED, got %s", updatedView.State)
	}
}

func TestPRCreateBlockedByGate(t *testing.T) {
	repo := setupTestGitRepo(t)
	mgr := NewManager(repo)

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}

	// Task contract only allows edits in packages/api, but the change lands in packages/core.
	_ = os.MkdirAll(filepath.Join(repo, ".agents"), 0755)
	_ = os.WriteFile(filepath.Join(repo, ".agents", "task.json"), []byte(`{"task_id": "", "allowed_paths": ["packages/api/**"]}`), 0644)
	runGit("checkout", "-b", "feat/out-of-scope")
	_ = os.WriteFile(filepath.Join(repo, "packages", "core", "billing.js"), []byte(`module.exports = { bill: () => 100 };`), 0644)
	runGit("add", "packages")
	runGit("commit", "-m", "feat: out of scope change")

	_, err := mgr.Create(CreateOptions{
		Title:      "feat(core): add billing module",
		HeadBranch: "feat/out-of-scope",
		BaseBranch: "main",
	})
	var gateErr *GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected PR creation to be blocked by gate, got err=%v", err)
	}
	if gateErr.ProofStatus != "FAILED" || len(gateErr.FailedChecks) == 0 || gateErr.FailedChecks[0].ID != "DoD-Boundary" {
		t.Fatalf("expected failed DoD-Boundary check, got %+v", gateErr)
	}
	if list, _ := mgr.List(ListOptions{State: "all"}); len(list) != 0 {
		t.Fatalf("expected no PR to be recorded when gate blocks, got %d", len(list))
	}

	// Drafts bypass the gate so work-in-progress can still be shared explicitly.
	draft, err := mgr.Create(CreateOptions{
		Title:      "feat(core): add billing module",
		HeadBranch: "feat/out-of-scope",
		BaseBranch: "main",
		Draft:      true,
	})
	if err != nil {
		t.Fatalf("expected draft PR creation to bypass gate, got %v", err)
	}
	if draft.State != StateDraft || draft.Proof.Status != "FAILED" {
		t.Fatalf("expected DRAFT PR carrying FAILED proof, got state=%s proof=%s", draft.State, draft.Proof.Status)
	}
}

func TestCheckPRGate(t *testing.T) {
	ok := &proof.ProofReceipt{Status: "VERIFIED"}
	if err := checkPRGate(ok, &review.AuditResult{Valid: true}); err != nil {
		t.Fatalf("expected verified proof + valid audit to pass, got %v", err)
	}
	if err := checkPRGate(ok, &review.AuditResult{Valid: false}); err == nil {
		t.Fatalf("expected invalid audit to block")
	}
	if err := checkPRGate(nil, nil); err == nil {
		t.Fatalf("expected missing proof to block")
	}
}
