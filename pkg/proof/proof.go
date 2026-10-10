package proof

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/deliver"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	"github.com/antigravity/monoflow/pkg/release"
	"github.com/antigravity/monoflow/pkg/task"
	"github.com/antigravity/monoflow/pkg/verify"
)

// DoDItem represents an executed Definition of Done checklist item.
type DoDItem struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Status      string  `json:"status"` // "PASSED" or "FAILED"
	DurationMs  float64 `json:"duration_ms"`
	Evidence    string  `json:"evidence"`
}

// ProofReceipt is the machine-checkable Proof of Work certificate for a PR.
type ProofReceipt struct {
	TaskID             string    `json:"task_id"`
	Status             string    `json:"status"` // "VERIFIED" or "FAILED"
	VerifiedAt         time.Time `json:"verified_at"`
	TotalDurationMs    float64   `json:"total_duration_ms"`
	OneSecondReviewCmd string    `json:"one_second_review_command"`
	GitTreeSHA         string    `json:"git_tree_sha"`
	DoDChecks          []DoDItem `json:"dod_checks"`
	AffectedPackages   []string  `json:"affected_packages"`
	DeliverableSHA     string    `json:"deliverable_sha,omitempty"`
}

// ProveTask runs the full DoD suite, builds deliverables, and emits the Proof Receipt.
func ProveTask(rootDir string, taskID string, g *graph.DependencyGraph, modifiedFiles []string) (*ProofReceipt, string, error) {
	start := time.Now()
	var dodItems []DoDItem

	// 1. Check Task Boundaries
	bStart := time.Now()
	taskBundlePath := filepath.Join(rootDir, ".agents", "task.json")
	var bundle task.TaskBundle
	hasBundle := false
	if data, err := os.ReadFile(taskBundlePath); err == nil {
		if json.Unmarshal(data, &bundle) == nil && (bundle.TaskID == taskID || bundle.TaskID == "") {
			hasBundle = true
		}
	}

	bDur := float64(time.Since(bStart).Microseconds()) / 1000.0
	if hasBundle && (len(bundle.AllowedPaths) > 0 || len(bundle.ReadOnlyPaths) > 0) {
		boundaryRes := task.CheckBoundaries(&bundle, modifiedFiles)
		if boundaryRes.Valid {
			dodItems = append(dodItems, DoDItem{
				ID:          "DoD-Boundary",
				Description: "Hermetic Scope & Read-Only Paths",
				Status:      "PASSED",
				DurationMs:  bDur,
				Evidence:    fmt.Sprintf("0 illegal path accesses across %d modified files", len(modifiedFiles)),
			})
		} else {
			dodItems = append(dodItems, DoDItem{
				ID:          "DoD-Boundary",
				Description: "Hermetic Scope & Read-Only Paths",
				Status:      "FAILED",
				DurationMs:  bDur,
				Evidence:    strings.Join(boundaryRes.Violations, "; "),
			})
		}
	} else {
		dodItems = append(dodItems, DoDItem{
			ID:          "DoD-Boundary",
			Description: "Hermetic Scope & Read-Only Paths",
			Status:      "PASSED",
			DurationMs:  bDur,
			Evidence:    "No restrictive path constraints violated",
		})
	}

	// 2. Test Integrity & Secret Leak Guard (Anti-Cheating & Security)
	gStart := time.Now()
	testViolations, _ := guard.CheckTestIntegrity(rootDir)
	secretViolations, _ := guard.ScanSecrets(rootDir)
	guardViolations := append(testViolations, secretViolations...)
	gDur := float64(time.Since(gStart).Microseconds()) / 1000.0

	if len(guardViolations) == 0 {
		dodItems = append(dodItems, DoDItem{
			ID:          "DoD-Guard",
			Description: "Test Integrity & Secret Scanning",
			Status:      "PASSED",
			DurationMs:  gDur,
			Evidence:    "0 assertions weakened or deleted; 0 secrets or API keys detected",
		})
	} else {
		var msgs []string
		for _, v := range guardViolations {
			msgs = append(msgs, fmt.Sprintf("[%s] %s: %s", v.Severity, v.File, v.Message))
		}
		dodItems = append(dodItems, DoDItem{
			ID:          "DoD-Guard",
			Description: "Test Integrity & Secret Scanning",
			Status:      "FAILED",
			DurationMs:  gDur,
			Evidence:    strings.Join(msgs, "; "),
		})
	}

	// 3. Affected Inner Loop Verification
	vStart := time.Now()
	runner := verify.NewRunner(rootDir)
	suiteRes := runner.VerifyAffected(g, modifiedFiles, verify.VerifyOptions{})
	vDur := float64(time.Since(vStart).Microseconds()) / 1000.0

	// Evidence names the exact commands that ran so the receipt cannot overstate coverage.
	var ran, failures []string
	for _, r := range suiteRes.Results {
		if r.CommandRun != "" {
			ran = append(ran, fmt.Sprintf("%s: %s", r.Target, r.CommandRun))
		}
		if r.Status == "failed" {
			msg := r.Target
			if len(r.Errors) > 0 {
				msg = fmt.Sprintf("%s: %s:%d %s", r.Target, r.Errors[0].File, r.Errors[0].Line, r.Errors[0].Message)
			}
			failures = append(failures, msg)
		}
	}
	if suiteRes.Status == "passed" {
		dodItems = append(dodItems, DoDItem{
			ID:          "DoD-InnerLoop",
			Description: "Affected Targets Typecheck, Lint & Unit Tests",
			Status:      "PASSED",
			DurationMs:  vDur,
			Evidence:    fmt.Sprintf("All %d affected packages passed in %0.1fms [%s]", suiteRes.TotalTargets, vDur, strings.Join(ran, "; ")),
		})
	} else {
		dodItems = append(dodItems, DoDItem{
			ID:          "DoD-InnerLoop",
			Description: "Affected Targets Typecheck, Lint & Unit Tests",
			Status:      "FAILED",
			DurationMs:  vDur,
			Evidence:    fmt.Sprintf("%d out of %d targets failed: %s", suiteRes.FailedTargets, suiteRes.TotalTargets, strings.Join(failures, "; ")),
		})
	}

	// 3b. Versioning: edges must resolve, and a declared changeset may not understate the
	// exported-API change (consumers rely on SemVer to upgrade safely at their own pace).
	dodItems = append(dodItems, versioningDoD(rootDir, g, modifiedFiles))

	// 3. Build Deliverable for Affected Packages
	var lastDeliverableSHA string
	for _, pkgName := range suiteRes.AffectedPackages {
		if pkg, ok := g.GetPackage(pkgName); ok {
			dStart := time.Now()
			manifest, err := deliver.BuildDeliverable(rootDir, pkg)
			dDur := float64(time.Since(dStart).Microseconds()) / 1000.0
			if err == nil && manifest != nil {
				lastDeliverableSHA = manifest.SHA256
				dodItems = append(dodItems, DoDItem{
					ID:          fmt.Sprintf("DoD-Build-%s", pkg.Name),
					Description: fmt.Sprintf("Hermetic Deliverable Assembly for %s", pkg.Name),
					Status:      "PASSED",
					DurationMs:  dDur,
					Evidence:    fmt.Sprintf("Compiled into .agents/dist/%s (SHA: %s)", pkg.Path, manifest.SHA256[:8]),
				})
			}
		}
	}

	// 4. Git Tree SHA
	gitTreeCmd := exec.Command("git", "write-tree")
	gitTreeCmd.Dir = rootDir
	treeOut, _ := gitTreeCmd.CombinedOutput()
	treeSHA := strings.TrimSpace(string(treeOut))

	overallStatus := "VERIFIED"
	for _, item := range dodItems {
		if item.Status == "FAILED" {
			overallStatus = "FAILED"
			break
		}
	}

	totalDur := float64(time.Since(start).Microseconds()) / 1000.0
	receipt := &ProofReceipt{
		TaskID:             taskID,
		Status:             overallStatus,
		VerifiedAt:         time.Now().UTC(),
		TotalDurationMs:    totalDur,
		OneSecondReviewCmd: fmt.Sprintf("monoflow verify --proof=%s", taskID),
		GitTreeSHA:         treeSHA,
		DoDChecks:          dodItems,
		AffectedPackages:   suiteRes.AffectedPackages,
		DeliverableSHA:     lastDeliverableSHA,
	}

	// Save receipt to .agents/proofs/<task-id>.json
	receiptPath := filepath.Join(rootDir, ".agents", "proofs", fmt.Sprintf("%s.json", taskID))
	_ = os.MkdirAll(filepath.Dir(receiptPath), 0755)
	receiptData, _ := json.MarshalIndent(receipt, "", "  ")
	_ = os.WriteFile(receiptPath, receiptData, 0644)

	// Generate Markdown Dossier for PR description
	dossier := FormatPRDossier(receipt)

	return receipt, dossier, nil
}

// FormatPRDossier generates the GitHub/GitLab markdown card.
func FormatPRDossier(r *ProofReceipt) string {
	var sb strings.Builder
	sb.WriteString("## 🛡️ Proof of Work & Definition of Done (DoD)\n")
	sb.WriteString(fmt.Sprintf("> ⚡ **1-Second Reviewer Command:** `%s` *(Verified in %0.1fms)*\n\n", r.OneSecondReviewCmd, r.TotalDurationMs))
	sb.WriteString("### ✅ Definition of Done Checklist\n")

	for _, item := range r.DoDChecks {
		checkMark := "[x]"
		if item.Status == "FAILED" {
			checkMark = "[ ]"
		}
		sb.WriteString(fmt.Sprintf("- %s **%s** (%0.1fms)\n", checkMark, item.Description, item.DurationMs))
		sb.WriteString(fmt.Sprintf("  - *Evidence:* %s\n", item.Evidence))
	}

	sb.WriteString("\n### 🔬 Instant Verification Command\n")
	sb.WriteString("```bash\n" + r.OneSecondReviewCmd + "\n```\n")

	return sb.String()
}

// LoadReceipt retrieves a saved proof receipt from disk.
func LoadReceipt(rootDir, taskID string) (*ProofReceipt, error) {
	receiptPath := filepath.Join(rootDir, ".agents", "proofs", fmt.Sprintf("%s.json", taskID))
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		cmd := exec.Command("git", "rev-parse", "--git-common-dir")
		cmd.Dir = rootDir
		if out, errG := cmd.CombinedOutput(); errG == nil {
			commonDir := strings.TrimSpace(string(out))
			if commonDir != "" {
				mainRoot := filepath.Dir(commonDir)
				candidate := filepath.Join(mainRoot, ".agents", "proofs", fmt.Sprintf("%s.json", taskID))
				data, err = os.ReadFile(candidate)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("proof receipt not found for task '%s': %w", taskID, err)
	}
	var r ProofReceipt
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func versioningDoD(rootDir string, g *graph.DependencyGraph, modifiedFiles []string) DoDItem {
	start := time.Now()
	item := DoDItem{ID: "DoD-Versioning", Description: "Internal dependency ranges & SemVer honesty", Status: "PASSED"}
	var problems, notes []string
	for _, e := range g.ScanEdges() {
		if e.Problem != "" {
			problems = append(problems, fmt.Sprintf("%s -> %s: %s", e.Consumer, e.Dependency, e.Problem))
		}
	}
	for _, f := range release.Tampered(rootDir, g) {
		problems = append(problems, "vendored release snapshot modified: "+f+" (releases are immutable; release a new version instead)")
	}
	declared := pendingBumps(rootDir)
	seen := map[string]bool{}
	for _, f := range modifiedFiles {
		p := g.MapFileToPackage(f)
		if p == nil || seen[p.Name] || graph.IsNonCodeFile(f) {
			continue
		}
		seen[p.Name] = true
		s, err := release.Suggest(rootDir, g, p.Name, "main")
		if err != nil || s.Required == "" {
			continue
		}
		if d, ok := declared[p.Name]; ok && bumpRank(d) < bumpRank(s.Required) {
			problems = append(problems, fmt.Sprintf("%s: changeset declares %s but the API diff requires %s (%d breaking/added symbols)", p.Name, d, s.Required, len(s.Changes)))
		} else if s.Required == "major" {
			notes = append(notes, fmt.Sprintf("%s has breaking API changes (requires a major bump on release)", p.Name))
		}
	}
	item.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	if len(problems) > 0 {
		item.Status = "FAILED"
		item.Evidence = strings.Join(problems, "; ")
	} else if len(notes) > 0 {
		item.Evidence = strings.Join(notes, "; ")
	} else {
		item.Evidence = "All internal ranges resolve; no undeclared breaking changes"
	}
	return item
}

func pendingBumps(rootDir string) map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(rootDir, ".agents", "changesets"))
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(rootDir, ".agents", "changesets", e.Name()))
		if err != nil {
			continue
		}
		var cs task.Changeset
		if json.Unmarshal(data, &cs) == nil && cs.Package != "" && bumpRank(cs.BumpType) > bumpRank(out[cs.Package]) {
			out[cs.Package] = cs.BumpType
		}
	}
	return out
}

func bumpRank(b string) int {
	switch strings.ToLower(b) {
	case "major":
		return 3
	case "minor":
		return 2
	case "patch":
		return 1
	}
	return 0
}
