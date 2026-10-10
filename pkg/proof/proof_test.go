package proof

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity/monoflow/pkg/graph"
)

func TestProveTaskAndLoadReceipt(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow_proof_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Setup minimal git repo
	agentsDir := filepath.Join(tmpDir, ".agents")
	_ = os.MkdirAll(agentsDir, 0755)

	pkgPath := filepath.Join(tmpDir, "services", "billing")
	_ = os.MkdirAll(pkgPath, 0755)

	g := graph.New(tmpDir)
	g.AddPackage(&graph.PackageNode{
		Name:     "services/billing",
		Path:     "services/billing",
		Language: graph.LangTypeScript,
	})

	receipt, dossier, err := ProveTask(tmpDir, "task-test-01", g, []string{"services/billing/tax.ts"})
	if err != nil {
		t.Fatalf("ProveTask failed: %v", err)
	}

	if receipt.TaskID != "task-test-01" {
		t.Errorf("unexpected task ID: %s", receipt.TaskID)
	}

	if !strings.Contains(dossier, "Proof of Work & Definition of Done") {
		t.Errorf("dossier missing header: %s", dossier)
	}

	// Verify LoadReceipt
	loaded, err := LoadReceipt(tmpDir, "task-test-01")
	if err != nil {
		t.Fatalf("LoadReceipt failed: %v", err)
	}
	if loaded.TaskID != receipt.TaskID {
		t.Errorf("loaded task ID mismatch")
	}
}

func TestFormatPRDossier(t *testing.T) {
	receipt := &ProofReceipt{
		TaskID:             "task-999",
		Status:             "VERIFIED",
		VerifiedAt:         time.Now(),
		TotalDurationMs:    320.5,
		OneSecondReviewCmd: "monoflow verify --proof=task-999",
		DoDChecks: []DoDItem{
			{
				ID:          "DoD-1",
				Description: "European VAT calculated correctly",
				Status:      "PASSED",
				DurationMs:  85.2,
				Evidence:    "tax.test.ts passed",
			},
		},
	}

	markdown := FormatPRDossier(receipt)
	if !strings.Contains(markdown, "monoflow verify --proof=task-999") {
		t.Errorf("markdown missing review command")
	}
	if !strings.Contains(markdown, "[x] **European VAT calculated correctly**") {
		t.Errorf("markdown missing checklist item")
	}
}
