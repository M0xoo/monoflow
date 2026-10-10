package review

import (
	"os"
	"testing"
)

func TestEvaluateRiskDocOnly(t *testing.T) {
	files := []string{"README.md", "docs/architecture.md"}
	res, err := EvaluateRisk(".", nil, files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Tier != Tier1AutoApprovable {
		t.Errorf("expected Tier1AutoApprovable, got %s", res.Tier)
	}
	if res.Score > 20 {
		t.Errorf("expected low score <= 20, got %d", res.Score)
	}
}

func TestEvaluateRiskCriticalFiles(t *testing.T) {
	files := []string{
		"services/billing/migrations/001_add_credits.sql",
		"services/auth/jwt_validator.go",
	}
	res, err := EvaluateRisk(".", nil, files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Tier != Tier3ElevatedRiskBlocked {
		t.Errorf("expected Tier3ElevatedRiskBlocked for migrations + auth, got %s", res.Tier)
	}
	if len(res.CriticalFiles) != 2 {
		t.Errorf("expected 2 critical files flagged, got %d", len(res.CriticalFiles))
	}
}

func TestAuditPRDossierHallucination(t *testing.T) {
	title := "Fix typo in README"
	desc := "Just updated the spelling in the introduction."
	files := []string{"README.md", "services/auth/jwt.go"}

	res := AuditPRDossier(title, desc, files)
	if res.Valid {
		t.Fatalf("expected audit failure due to code modified under 'typo' title")
	}

	if len(res.HallucinationWarnings) == 0 {
		t.Errorf("expected hallucination warnings, got 0")
	}

	if len(res.UndeclaredFiles) == 0 {
		t.Errorf("expected undeclared critical file 'services/auth/jwt.go'")
	}
}

func TestAuditPRDossierAccurate(t *testing.T) {
	title := "Update JWT authentication token expiration"
	desc := "Refactored services/auth jwt handling to extend session timeout."
	files := []string{"services/auth/jwt.go"}

	res := AuditPRDossier(title, desc, files)
	if !res.Valid {
		t.Fatalf("expected audit to pass for accurate disclosure, got warnings: %+v", res.HallucinationWarnings)
	}
}

func TestBackpressureThrottling(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow-backpressure-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Under limit
	status, err := CheckBackpressure(tmpDir, 2)
	if err != nil || status.Throttled {
		t.Fatalf("expected not throttled initially: %+v", status)
	}

	// Add 2 PRs
	_ = RecordActivePR(tmpDir, "PR-101", "feat: auth", 25, Tier2HumanReviewRequired)
	_ = RecordActivePR(tmpDir, "PR-102", "feat: billing", 30, Tier2HumanReviewRequired)

	// Now check again
	status, err = CheckBackpressure(tmpDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Throttled {
		t.Fatalf("expected backpressure throttling at limit 2, but was not throttled")
	}

	// Clear one PR
	_ = ClearActivePR(tmpDir, "PR-101")
	status, _ = CheckBackpressure(tmpDir, 2)
	if status.Throttled {
		t.Fatalf("expected throttle to lift after clearing PR")
	}
}
