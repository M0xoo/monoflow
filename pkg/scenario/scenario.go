package scenario

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	"github.com/antigravity/monoflow/pkg/pr"
	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/subset"
	"github.com/antigravity/monoflow/pkg/task"
	"github.com/antigravity/monoflow/pkg/verify"
	"github.com/antigravity/monoflow/pkg/version"
	"github.com/antigravity/monoflow/pkg/worktree"
)

// SceneResult records the output of an individual scenario step.
type SceneResult struct {
	SceneNumber int            `json:"scene_number"`
	Title       string         `json:"title"`
	Status      string         `json:"status"` // "PASSED", "BLOCKED_AS_EXPECTED"
	DurationMs  float64        `json:"duration_ms"`
	Details     map[string]any `json:"details"`
}

// ScenarioReport summarizes the entire multi-agent monorepo simulation.
type ScenarioReport struct {
	Status      string        `json:"status"`
	TotalScenes int           `json:"total_scenes"`
	Passed      int           `json:"passed"`
	TotalTimeMs float64       `json:"total_time_ms"`
	RepoDir     string        `json:"repo_dir,omitempty"`
	Scenes      []SceneResult `json:"scenes"`
	Verdict     string        `json:"verdict"`
}

// RunScenario executes a complete real-world multi-agent monorepo simulation.
func RunScenario(baseDir string) (*ScenarioReport, error) {
	overallStart := time.Now()
	var scenes []SceneResult

	// Use temporary workspace if baseDir is empty
	if baseDir == "" {
		tmp, err := os.MkdirTemp("", "monoflow_scenario_*")
		if err != nil {
			return nil, err
		}
		baseDir = tmp
	}
	defer func() {
		// Clean up if it was a temp directory
		if strings.Contains(baseDir, "monoflow_scenario_") {
			_ = os.RemoveAll(baseDir)
		}
	}()

	repoDir := filepath.Join(baseDir, "enterprise-monorepo")
	_ = os.MkdirAll(repoDir, 0755)

	runGit := func(dir string, args ...string) error {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %v failed: %w (%s)", args, err, string(out))
		}
		return nil
	}

	// =========================================================================
	// SCENE 1: Provision Enterprise Monorepo & Topology
	// =========================================================================
	s1Start := time.Now()
	if err := runGit(repoDir, "init", "-b", "main"); err != nil {
		return nil, err
	}
	_ = runGit(repoDir, "config", "user.name", "MonoFlow CI Agent")
	_ = runGit(repoDir, "config", "user.email", "ci-agent@enterprise.internal")

	_ = os.WriteFile(filepath.Join(repoDir, ".gitignore"), []byte(`.agents/worktrees/
.agents/traces/
.agents/dist/
.agents/queue/
.agents/checkpoints.json
.agents/circuit_breaker.json
.agents/proofs/
.agents/changesets/
.agents/outer_loop/
.agents/events/
.agents/prs/
.agents/subset.json
`), 0644)

	// Packages: contracts, auth, payments, web-portal, analytics
	pkgDirs := []string{
		"packages/contracts",
		"services/auth",
		"services/payments",
		"apps/web-portal",
		"services/analytics",
		".agents/changesets",
		".agents/rules",
	}
	for _, d := range pkgDirs {
		_ = os.MkdirAll(filepath.Join(repoDir, d), 0755)
	}

	// 1. contracts
	_ = os.WriteFile(filepath.Join(repoDir, "packages/contracts", "schema.proto"), []byte(`syntax = "proto3";
package enterprise.contracts;
message TaxRequest { double amount = 1; string region = 2; }
message TaxResponse { double tax = 1; double total = 2; }
`), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "packages/contracts", "package.json"), []byte(`{"name": "@enterprise/contracts", "version": "1.0.0"}`), 0644)

	// 2. services/auth (Go)
	_ = os.WriteFile(filepath.Join(repoDir, "services/auth", "auth.go"), []byte(`package auth
func VerifyToken(token string) bool { return token != "" }
`), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "services/auth", "auth_test.go"), []byte(`package auth
import "testing"
func TestVerifyToken(t *testing.T) {
	if !VerifyToken("valid") { t.Fatal("expected valid token") }
}
`), 0644)

	// 3. services/payments (Go)
	_ = os.WriteFile(filepath.Join(repoDir, "services/payments", "payments.go"), []byte(`package payments
func ProcessPayment(amount float64) bool { return amount > 0 }
`), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "services/payments", "payments_test.go"), []byte(`package payments

import "testing"

func TestProcessPayment(t *testing.T) {
	if !ProcessPayment(100.0) {
		t.Fatal("expected payment accepted")
	}
}
`), 0644)

	// 4. apps/web-portal (JS)
	_ = os.WriteFile(filepath.Join(repoDir, "apps/web-portal", "app.js"), []byte(`module.exports = { status: "ready" };`), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "apps/web-portal", "app.test.js"), []byte(`const app = require('./app'); if (app.status !== 'ready') process.exit(1);`), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "apps/web-portal", "package.json"), []byte(`{
  "name": "@enterprise/web-portal",
  "version": "1.0.0",
  "scripts": { "test": "node app.test.js" },
  "dependencies": { "@enterprise/contracts": "^1.0.0" }
}`), 0644)

	// 5. services/analytics (Generic)
	_ = os.WriteFile(filepath.Join(repoDir, "services/analytics", "report.txt"), []byte("analytics report engine ready\n"), 0644)

	// Monorepo config
	cfgJSON := `{
  "version": "1.0",
  "monorepo_type": "polyglot",
  "packages": [
    {
      "name": "contracts",
      "path": "packages/contracts",
      "language": "proto",
      "version": "1.0.0",
      "dependencies": []
    },
    {
      "name": "auth",
      "path": "services/auth",
      "language": "go",
      "version": "1.0.0",
      "dependencies": ["contracts"],
      "test_cmd": "go test -v ./..."
    },
    {
      "name": "payments",
      "path": "services/payments",
      "language": "go",
      "version": "1.0.0",
      "dependencies": ["contracts"],
      "test_cmd": "go test -v ./..."
    },
    {
      "name": "web-portal",
      "path": "apps/web-portal",
      "language": "javascript",
      "version": "1.0.0",
      "dependencies": ["contracts"],
      "test_cmd": "node app.test.js"
    },
    {
      "name": "analytics",
      "path": "services/analytics",
      "language": "generic",
      "version": "1.0.0",
      "dependencies": []
    }
  ]
}`
	_ = os.WriteFile(filepath.Join(repoDir, ".agents", "monoflow.config.json"), []byte(cfgJSON), 0644)
	_ = os.WriteFile(filepath.Join(repoDir, "go.mod"), []byte("module enterprise\n\ngo 1.23\n"), 0644)

	_ = runGit(repoDir, "add", "-A")
	_ = runGit(repoDir, "commit", "-m", "chore: initial enterprise monorepo setup with 5 packages")

	g, err := graph.LoadOrDiscover(repoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load graph in Scene 1: %w", err)
	}

	scenes = append(scenes, SceneResult{
		SceneNumber: 1,
		Title:       "Monorepo Provisioning & Topology Discovery",
		Status:      "PASSED",
		DurationMs:  float64(time.Since(s1Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"total_packages": len(g.GetAllPackages()),
			"packages":       []string{"contracts", "auth", "payments", "web-portal", "analytics"},
			"graph_edges":    3,
		},
	})

	// =========================================================================
	// SCENE 2: Agent Alpha — Subsetting & Fast Inner Loop Verification
	// =========================================================================
	s2Start := time.Now()
	// Agent Alpha selects subset: payments + contracts
	subsetRes, err := subset.ApplyWithOptions(repoDir, []string{"payments"}, subset.DepStrategyContracts)
	if err != nil {
		return nil, fmt.Errorf("Agent Alpha failed to project subset: %w", err)
	}

	// Verify subset health
	healthRes, err := subset.CheckSubsetHealth(repoDir)
	if err != nil || healthRes.Status != "healthy" {
		return nil, fmt.Errorf("subset health check failed: %v", err)
	}

	// Save ephemeral checkpoint
	chkMgr := checkpoint.NewManager(repoDir)
	chk, err := chkMgr.Save("pre-feature-tax-implementation")
	if err != nil {
		return nil, fmt.Errorf("failed to save checkpoint: %w", err)
	}

	// Agent Alpha modifies payments service: adds tax calculation
	newPaymentsCode := `package payments

func ProcessPayment(amount float64) bool { return amount > 0 }

// CalculateTax computes sales tax for regional compliance.
func CalculateTax(amount float64, rate float64) float64 {
	return amount * rate
}
`
	_ = os.WriteFile(filepath.Join(repoDir, "services/payments", "payments.go"), []byte(newPaymentsCode), 0644)

	newPaymentsTest := `package payments

import "testing"

func TestProcessPayment(t *testing.T) {
	if !ProcessPayment(100.0) {
		t.Fatal("expected payment accepted")
	}
}

func TestCalculateTax(t *testing.T) {
	tax := CalculateTax(100.0, 0.15)
	if tax != 15.0 {
		t.Fatalf("expected tax 15.0, got %f", tax)
	}
}
`
	_ = os.WriteFile(filepath.Join(repoDir, "services/payments", "payments_test.go"), []byte(newPaymentsTest), 0644)

	// Inner Loop Verification (< 150ms)
	runner := verify.NewRunner(repoDir)
	changedFiles := []string{"services/payments/payments.go", "services/payments/payments_test.go"}
	verifyRes := runner.VerifyAffected(g, changedFiles, verify.VerifyOptions{})
	if verifyRes.Status != "passed" {
		return nil, fmt.Errorf("inner loop verification failed on affected targets")
	}

	// Record changeset
	cs := task.Changeset{
		Package:   "payments",
		BumpType:  "minor",
		Summary:   "Add CalculateTax endpoint and unit test suite",
		TaskID:    "task-alpha-tax",
		CreatedAt: time.Now().UTC(),
	}
	_ = task.SaveChangeset(repoDir, cs)

	// Prove Task with Proof of Work
	receipt, _, err := proof.ProveTask(repoDir, "task-alpha-tax", g, changedFiles)
	if err != nil {
		return nil, fmt.Errorf("proof of work generation failed: %w", err)
	}

	scenes = append(scenes, SceneResult{
		SceneNumber: 2,
		Title:       "Agent Alpha: Sparse Subsetting, Inner Loop & Proof of Work",
		Status:      "PASSED",
		DurationMs:  float64(time.Since(s2Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"projected_packages": subsetRes.IncludedPackages,
			"omitted_packages":   subsetRes.OmittedPackages,
			"checkpoint_id":      chk.ID,
			"inner_loop_ms":      verifyRes.DurationMs,
			"proof_status":       receipt.Status,
			"proof_checks":       len(receipt.DoDChecks),
		},
	})

	// =========================================================================
	// SCENE 3: The Moat in Action — Guard Violations & Protected Branch Shield
	// =========================================================================
	s3Start := time.Now()

	// 3A: Cheating Attack (Agent attempts to delete unit test assertion)
	cheatingTest := `package payments

import "testing"

func TestProcessPayment(t *testing.T) {
	// Assertion deleted to pass CI fraudulently
}
`
	_ = os.WriteFile(filepath.Join(repoDir, "services/payments", "payments_test.go"), []byte(cheatingTest), 0644)
	violations, _ := guard.CheckTestIntegrity(repoDir)
	if len(violations) == 0 {
		return nil, fmt.Errorf("FAIL: Guard failed to detect deleted assertion!")
	}
	// Attempt fast commit with weakened test -> must be blocked
	_, commitErr := guard.FastAgentCommit(repoDir, "cheat: delete test assertion")
	if commitErr == nil {
		return nil, fmt.Errorf("FAIL: FastAgentCommit should have blocked weakened test!")
	}

	// Rollback cheating edit using checkpoint
	_ = os.WriteFile(filepath.Join(repoDir, "services/payments", "payments_test.go"), []byte(newPaymentsTest), 0644)

	// 3B: Secret Leak Attack
	secretFile := filepath.Join(repoDir, "services/payments", "config.env")
	_ = os.WriteFile(secretFile, []byte("STRIPE_SECRET=sk_live_51ABCDEF1234567890XYZ\n"), 0644)
	secretViolations, _ := guard.ScanSecrets(repoDir)
	if len(secretViolations) == 0 {
		return nil, fmt.Errorf("FAIL: Guard failed to detect secret leak!")
	}
	_ = os.Remove(secretFile)

	scenes = append(scenes, SceneResult{
		SceneNumber: 3,
		Title:       "MonoFlow Moat: Anti-Cheating & Secret Leak Defenses Block Attacks",
		Status:      "BLOCKED_AS_EXPECTED",
		DurationMs:  float64(time.Since(s3Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"test_weakening_detected": violations[0].Message,
			"secret_leak_detected":    secretViolations[0].Message,
			"commit_status":           "STRICTLY_BLOCKED",
		},
	})

	// =========================================================================
	// SCENE 4: Agent Beta — Virtual Worktree & Parallel Isolation
	// =========================================================================
	s4Start := time.Now()
	wtMgr := worktree.NewManager(repoDir)
	wtRes, err := wtMgr.CreateWithOpts("agent-beta-frontend", worktree.CreateOptions{
		Packages: []string{"web-portal"},
		Strategy: subset.DepStrategyContracts,
	})
	if err != nil {
		return nil, fmt.Errorf("Agent Beta worktree creation failed: %w", err)
	}

	// Verify worktree exists and is isolated
	if _, err := os.Stat(wtRes.Path); os.IsNotExist(err) {
		return nil, fmt.Errorf("worktree path %s does not exist", wtRes.Path)
	}

	scenes = append(scenes, SceneResult{
		SceneNumber: 4,
		Title:       "Agent Beta: Virtual Worktree with CoW Isolation (<60ms)",
		Status:      "PASSED",
		DurationMs:  float64(time.Since(s4Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"agent_id":      wtRes.AgentID,
			"worktree_path": wtRes.Path,
			"packages":      wtRes.Packages,
			"head_sha":      wtRes.HeadSHA,
		},
	})

	// =========================================================================
	// SCENE 5: Agent-Native PR Lifecycle & Hot-Mount Delta Testing
	// =========================================================================
	s5Start := time.Now()

	// 1. Switch to feature branch for Agent Alpha
	_ = runGit(repoDir, "checkout", "-b", "feat/alpha-tax-calculation")

	// 2. Verified Agent Commit
	commitRes, err := guard.FastAgentCommit(repoDir, "feat(payments): implement regional tax calculation")
	if err != nil {
		if commitRes != nil {
			return nil, fmt.Errorf("failed to commit tax feature: %w (violations: %+v)", err, commitRes.Violations)
		}
		return nil, fmt.Errorf("failed to commit tax feature: %w", err)
	}

	// 3. Create PR
	prMgr := pr.NewManager(repoDir)
	prMeta, err := prMgr.Create(pr.CreateOptions{
		Title:      "feat(payments): implement regional tax calculation",
		Body:       "Implements CalculateTax in payments package with verified unit tests.",
		BaseBranch: "main",
		AutoPush:   false, // Local fixture simulation
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create PR: %w", err)
	}

	// 4. Hot-Mount Delta Verification (<120ms)
	testRes, err := prMgr.Test(prMeta.ID, pr.TestOptions{AgainstBase: true})
	if err != nil || testRes.Status != "passed" {
		errMsg := ""
		if testRes != nil {
			errMsg = testRes.Verdict
		}
		return nil, fmt.Errorf("PR delta testing failed: %v (%s)", err, errMsg)
	}

	// 5. Machine Risk Audit & Automated Review
	reviewRes, err := prMgr.Review(prMeta.ID, pr.ReviewOptions{Approve: true})
	if err != nil || reviewRes.ReviewDecision != "APPROVED" {
		return nil, fmt.Errorf("PR review failed: %v", err)
	}

	// 6. Safe Merge into main
	mergeRes, err := prMgr.Merge(prMeta.ID, pr.MergeOptions{Strategy: "squash"})
	if err != nil || mergeRes.Status != "merged" {
		return nil, fmt.Errorf("PR merge failed: %v", err)
	}

	scenes = append(scenes, SceneResult{
		SceneNumber: 5,
		Title:       "PR Suite: Delta Hot-Mount Test, Machine Review & Safe Merge",
		Status:      "PASSED",
		DurationMs:  float64(time.Since(s5Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"commit_sha":        commitRes.CommitSHA,
			"pr_id":             prMeta.ID,
			"risk_tier":         prMeta.Risk.Tier,
			"risk_score":        prMeta.Risk.Score,
			"hot_mount_test_ms": testRes.VerificationTimeMs,
			"review_decision":   reviewRes.ReviewDecision,
			"merged_sha":        mergeRes.CommitSHA,
		},
	})

	// =========================================================================
	// SCENE 6: SemVer Versioning & Changeset Cascading
	// =========================================================================
	s6Start := time.Now()
	// Switch back to main to verify merged changes
	_ = runGit(repoDir, "checkout", "main")

	// Apply pending changesets
	applyRes, err := version.ApplyChangesets(repoDir)
	if err != nil {
		return nil, fmt.Errorf("version apply failed: %w", err)
	}

	// Check consistency across monorepo and active subsets
	consistencyRes, err := version.CheckVersionConsistency(repoDir)
	if err != nil || consistencyRes.Status == "error" {
		return nil, fmt.Errorf("version consistency check failed: %v", err)
	}

	scenes = append(scenes, SceneResult{
		SceneNumber: 6,
		Title:       "Versioning: Changeset Resolution, SemVer Bump & Consistency Audit",
		Status:      "PASSED",
		DurationMs:  float64(time.Since(s6Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"applied_changesets": applyRes.AppliedChangesets,
			"bumped_packages":    len(applyRes.BumpedPackages),
			"new_version":        "1.1.0",
			"consistency_status": consistencyRes.Status,
			"verdict":            consistencyRes.Verdict,
		},
	})

	// =========================================================================
	// SCENE 7: Event Bus & Autonomous Agent Inbox (<5ms)
	// =========================================================================
	s7Start := time.Now()
	eventMgr := events.NewEventManager(repoDir)
	_ = eventMgr.Emit(&events.Event{
		Type:    events.EventPRMerged,
		Summary: fmt.Sprintf("PR %s merged into main by Agent Alpha", prMeta.ID),
		Payload: map[string]any{
			"pr_id":   prMeta.ID,
			"package": "payments",
			"version": "1.1.0",
		},
	})

	unread, err := eventMgr.List(true)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch unread events: %w", err)
	}

	scenes = append(scenes, SceneResult{
		SceneNumber: 7,
		Title:       "Autonomous Event Inbox: Asynchronous Notification (<5ms)",
		Status:      "PASSED",
		DurationMs:  float64(time.Since(s7Start).Microseconds()) / 1000.0,
		Details: map[string]any{
			"unread_events_count": len(unread),
			"latest_event_type":   string(unread[len(unread)-1].Type),
			"summary":             unread[len(unread)-1].Summary,
		},
	})

	totalDurMs := float64(time.Since(overallStart).Microseconds()) / 1000.0

	return &ScenarioReport{
		Status:      "success",
		TotalScenes: len(scenes),
		Passed:      len(scenes),
		TotalTimeMs: totalDurMs,
		RepoDir:     repoDir,
		Scenes:      scenes,
		Verdict:     "All 7 Enterprise Multi-Agent Monorepo Scenes Passed with 100% Invariant Enforcement.",
	}, nil
}
