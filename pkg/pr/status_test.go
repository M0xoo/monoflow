package pr

import (
	"strings"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/review"
)

func TestExtractGitHubRepo(t *testing.T) {
	tests := []struct {
		url       string
		wantOwner string
		wantRepo  string
		wantOk    bool
	}{
		{"https://github.com/M0xoo/monoflow.git", "M0xoo", "monoflow", true},
		{"https://github.com/M0xoo/monoflow", "M0xoo", "monoflow", true},
		{"git@github.com:M0xoo/monoflow.git", "M0xoo", "monoflow", true},
		{"git@github.com:M0xoo/monoflow", "M0xoo", "monoflow", true},
		{"ssh://git@github.com/M0xoo/monoflow.git", "M0xoo", "monoflow", true},
		{"https://gitlab.com/M0xoo/monoflow.git", "", "", false},
		{"invalid-url", "", "", false},
	}

	for _, tt := range tests {
		owner, repo, ok := extractGitHubRepo(tt.url)
		if ok != tt.wantOk || owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("extractGitHubRepo(%q) = (%q, %q, %v); want (%q, %q, %v)",
				tt.url, owner, repo, ok, tt.wantOwner, tt.wantRepo, tt.wantOk)
		}
	}
}

func TestPRStatusBridged(t *testing.T) {
	repo := setupTestGitRepo(t)
	mgr := NewManager(repo)

	// Create test PR metadata with both local proof and remote GitHub URL
	prID := "pr-42"
	metadata := &PRMetadata{
		ID:           prID,
		GitHubNumber: 42,
		GitHubURL:    "https://github.com/monoflow-test/repo/pull/42",
		Title:        "feat(billing): add high-speed billing primitives",
		HeadBranch:   "feat/billing",
		BaseBranch:   "main",
		Author:       "Test Agent",
		State:        StateOpen,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		HeadSHA:      "head1234sha",
		BaseSHA:      "base1234sha",
		AffectedPackages: []string{
			"core",
			"api",
		},
		Risk: &review.RiskAssessment{
			Score:                 10,
			Tier:                  review.Tier1AutoApprovable,
			Verdict:               "Low Risk: Eligible for fast-track review",
			DownstreamImpactCount: 1,
		},
		Proof: &proof.ProofReceipt{
			TaskID:             "feat/billing",
			Status:             "VERIFIED",
			VerifiedAt:         time.Now().UTC(),
			TotalDurationMs:    45.2,
			OneSecondReviewCmd: "monoflow verify --proof=feat/billing",
			GitTreeSHA:         "tree5678sha",
			DoDChecks: []proof.DoDItem{
				{
					ID:          "DoD-Boundary",
					Description: "Hermetic Scope & Read-Only Paths",
					Status:      "PASSED",
					DurationMs:  0.2,
					Evidence:    "0 illegal path accesses",
				},
				{
					ID:          "DoD-InnerLoop",
					Description: "Affected Targets Typecheck & Unit Tests",
					Status:      "PASSED",
					DurationMs:  45.0,
					Evidence:    "All 2 packages passed in 45.0ms",
				},
			},
			AffectedPackages: []string{"core", "api"},
		},
		Audit: &review.AuditResult{
			Valid:   true,
			Verdict: "Zero hallucination detected",
		},
	}

	if err := mgr.save(metadata); err != nil {
		t.Fatalf("Failed to save test PR: %v", err)
	}

	// Query Status
	status, err := mgr.Status("42")
	if err != nil {
		t.Fatalf("Status failed for PR #42: %v", err)
	}

	if status.PRID != prID {
		t.Errorf("expected PRID %s, got %s", prID, status.PRID)
	}
	if status.GitHubURL != "https://github.com/monoflow-test/repo/pull/42" {
		t.Errorf("expected GitHubURL to match, got %s", status.GitHubURL)
	}
	if status.GitHubNumber != 42 {
		t.Errorf("expected GitHubNumber 42, got %d", status.GitHubNumber)
	}
	if status.ProofStatus != "VERIFIED" {
		t.Errorf("expected ProofStatus VERIFIED, got %s", status.ProofStatus)
	}
	if status.BridgeStatus != "BRIDGED" {
		t.Errorf("expected BridgeStatus BRIDGED, got %s", status.BridgeStatus)
	}
	if status.DoDPassedChecks != 2 || status.DoDTotalChecks != 2 {
		t.Errorf("expected 2/2 DoD checks passed, got %d/%d", status.DoDPassedChecks, status.DoDTotalChecks)
	}

	// Verify formatted single view contains both proof receipt and remote GitHub URL
	view := status.FormatView()
	if !strings.Contains(view, "https://github.com/monoflow-test/repo/pull/42") {
		t.Errorf("expected formatted view to contain GitHub URL")
	}
	if !strings.Contains(view, "DoD Proof Receipt: ✅ VERIFIED") {
		t.Errorf("expected formatted view to contain DoD proof receipt")
	}
	if !strings.Contains(view, "Hermetic Scope & Read-Only Paths") {
		t.Errorf("expected formatted view to contain DoD criteria")
	}
	if !strings.Contains(view, "Tier 1") {
		t.Errorf("expected formatted view to contain risk tier")
	}
}

func TestPRStatusLocalOnly(t *testing.T) {
	repo := setupTestGitRepo(t)
	mgr := NewManager(repo)

	prID := "pr-local-99"
	metadata := &PRMetadata{
		ID:         prID,
		Title:      "feat(internal): local agent proof test",
		HeadBranch: "feat/internal",
		BaseBranch: "main",
		Author:     "Agent Local",
		State:      StateOpen,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Proof: &proof.ProofReceipt{
			TaskID:          "feat/internal",
			Status:          "VERIFIED",
			TotalDurationMs: 12.5,
			GitTreeSHA:      "tree999sha",
			DoDChecks: []proof.DoDItem{
				{
					ID:          "DoD-Guard",
					Description: "Test Integrity & Secret Scanning",
					Status:      "PASSED",
					DurationMs:  12.0,
					Evidence:    "0 secrets detected",
				},
			},
		},
	}

	_ = mgr.save(metadata)

	status, err := mgr.Status(prID)
	if err != nil {
		t.Fatalf("Status failed for %s: %v", prID, err)
	}

	if status.BridgeStatus != "LOCAL_ONLY" {
		t.Errorf("expected BridgeStatus LOCAL_ONLY, got %s", status.BridgeStatus)
	}
	if status.GitHubURL != "" {
		t.Errorf("expected empty GitHub URL for local-only PR, got %s", status.GitHubURL)
	}
	if status.ProofStatus != "VERIFIED" {
		t.Errorf("expected ProofStatus VERIFIED, got %s", status.ProofStatus)
	}

	view := status.FormatView()
	if !strings.Contains(view, "Not Linked (Local-only PR dossier)") {
		t.Errorf("expected formatted view to note local-only state")
	}
	if !strings.Contains(view, "DoD Proof Receipt: ✅ VERIFIED") {
		t.Errorf("expected formatted view to contain DoD proof receipt")
	}
}

func TestPRStatusAutoResolve(t *testing.T) {
	repo := setupTestGitRepo(t)
	mgr := NewManager(repo)

	// Save an open PR
	openPR := &PRMetadata{
		ID:         "pr-active-1",
		Title:      "feat(active): active pull request",
		HeadBranch: "feat/active",
		BaseBranch: "main",
		State:      StateOpen,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	_ = mgr.save(openPR)

	// Status("") should auto-resolve to openPR
	status, err := mgr.Status("")
	if err != nil {
		t.Fatalf("Status(\"\") auto-resolve failed: %v", err)
	}
	if status.PRID != "pr-active-1" {
		t.Errorf("expected auto-resolved PRID pr-active-1, got %s", status.PRID)
	}
}
