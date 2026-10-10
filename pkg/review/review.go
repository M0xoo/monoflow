package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/antigravity/monoflow/pkg/graph"
)

// RiskTier categorizes the review necessity of an agent-generated PR.
type RiskTier string

const (
	Tier1AutoApprovable        RiskTier = "TIER_1_AUTO_APPROVABLE"
	Tier2HumanReviewRequired   RiskTier = "TIER_2_HUMAN_REVIEW_REQUIRED"
	Tier3ElevatedRiskBlocked   RiskTier = "TIER_3_ELEVATED_RISK_BLOCKED"
)

// RiskFactor explains why risk points were assigned.
type RiskFactor struct {
	Category string `json:"category"`
	Delta    int    `json:"delta"`
	Reason   string `json:"reason"`
}

// RiskAssessment summarizes the machine-calculated risk score and tier.
type RiskAssessment struct {
	Score                 int          `json:"score"`
	Tier                  RiskTier     `json:"tier"`
	Factors               []RiskFactor `json:"factors"`
	CriticalFiles         []string     `json:"critical_files,omitempty"`
	DownstreamImpactCount int          `json:"downstream_impact_count"`
	TotalFilesChanged     int          `json:"total_files_changed"`
	Verdict               string       `json:"verdict"`
}

// AuditResult records discrepancies between the PR's claimed intent and actual Git diff.
type AuditResult struct {
	Valid                 bool     `json:"valid"`
	UndeclaredFiles       []string `json:"undeclared_files,omitempty"`
	HallucinationWarnings []string `json:"hallucination_warnings,omitempty"`
	Verdict               string   `json:"verdict"`
}

// BackpressureStatus represents whether the human review bandwidth is saturated.
type BackpressureStatus struct {
	Throttled         bool   `json:"throttled"`
	ActivePRCount     int    `json:"active_pr_count"`
	MaxAllowedPRs     int    `json:"max_allowed_prs"`
	Reason            string `json:"reason"`
	RecommendedAction string `json:"recommended_action"`
}

var (
	migrationPattern = regexp.MustCompile(`(?i)(migrations?/|schema\.sql$|\.prisma$|alembic/|flyway/)`)
	securityPattern  = regexp.MustCompile(`(?i)(auth/|oauth/|jwt/|crypto/|security/|permissions?/|rbac/)`)
	infraPattern     = regexp.MustCompile(`(?i)(\.github/workflows/|dockerfile|docker-compose|k8s/|terraform/|helm/)`)
	secretPattern    = regexp.MustCompile(`(?i)(\.env|credentials|secret|vault)`)
	docOnlyPattern   = regexp.MustCompile(`(?i)(\.md|\.txt|\.markdown|docs/)$`)
	testPattern      = regexp.MustCompile(`(?i)(_test\.go|\.spec\.ts|\.test\.ts|\.test\.js|_test\.py|test_.*\.py)$`)
)

// EvaluateRisk calculates a machine-verifiable risk score from 0 to 100.
func EvaluateRisk(rootDir string, g *graph.DependencyGraph, changedFiles []string) (*RiskAssessment, error) {
	score := 0
	var factors []RiskFactor
	var criticalFiles []string

	if len(changedFiles) == 0 {
		return &RiskAssessment{
			Score:             0,
			Tier:              Tier1AutoApprovable,
			TotalFilesChanged: 0,
			Verdict:           "No files changed (zero risk)",
		}, nil
	}

	// 1. Check if all changes are purely documentation
	allDocs := true
	allTests := true
	hasTests := false
	hasImplementation := false

	for _, file := range changedFiles {
		isDoc := docOnlyPattern.MatchString(file)
		isTest := testPattern.MatchString(file)

		if !isDoc {
			allDocs = false
		}
		if !isTest {
			allTests = false
			hasImplementation = true
		} else {
			hasTests = true
		}

		// Check critical file patterns
		if migrationPattern.MatchString(file) {
			score += 35
			criticalFiles = append(criticalFiles, file)
			factors = append(factors, RiskFactor{
				Category: "DATABASE_MIGRATION",
				Delta:    35,
				Reason:   fmt.Sprintf("Touches database schema or migration: %s", file),
			})
		}

		if securityPattern.MatchString(file) {
			score += 30
			criticalFiles = append(criticalFiles, file)
			factors = append(factors, RiskFactor{
				Category: "SECURITY_AND_AUTH",
				Delta:    30,
				Reason:   fmt.Sprintf("Touches security or authentication logic: %s", file),
			})
		}

		if infraPattern.MatchString(file) {
			score += 25
			criticalFiles = append(criticalFiles, file)
			factors = append(factors, RiskFactor{
				Category: "INFRASTRUCTURE_AND_CI",
				Delta:    25,
				Reason:   fmt.Sprintf("Touches CI/CD workflow or infrastructure: %s", file),
			})
		}

		if secretPattern.MatchString(file) {
			score += 40
			criticalFiles = append(criticalFiles, file)
			factors = append(factors, RiskFactor{
				Category: "SECRETS_AND_CONFIG",
				Delta:    40,
				Reason:   fmt.Sprintf("Touches environment or credential template: %s", file),
			})
		}
	}

	if allDocs {
		return &RiskAssessment{
			Score:             5,
			Tier:              Tier1AutoApprovable,
			Factors:           []RiskFactor{{Category: "DOCUMENTATION_ONLY", Delta: 5, Reason: "Changes are strictly documentation"}},
			TotalFilesChanged: len(changedFiles),
			Verdict:           "Documentation only — Safe for automated instant approval",
		}, nil
	}

	if allTests {
		return &RiskAssessment{
			Score:             15,
			Tier:              Tier1AutoApprovable,
			Factors:           []RiskFactor{{Category: "TESTS_ONLY", Delta: 15, Reason: "Changes are strictly unit or integration tests"}},
			TotalFilesChanged: len(changedFiles),
			Verdict:           "Test suite only — Safe for automated approval if tests pass",
		}, nil
	}

	// Test deficit penalty
	if hasImplementation && !hasTests {
		score += 20
		factors = append(factors, RiskFactor{
			Category: "TEST_DEFICIT",
			Delta:    20,
			Reason:   "Implementation code modified without any accompanying tests",
		})
	}

	// Downstream impact calculation using DAG
	downstreamCount := 0
	if g != nil {
		affected := g.ComputeAffected(changedFiles)
		downstreamCount = len(affected.TransitiveAffected)
		if downstreamCount > 0 {
			fanoutScore := downstreamCount * 5
			if fanoutScore > 30 {
				fanoutScore = 30
			}
			score += fanoutScore
			factors = append(factors, RiskFactor{
				Category: "DOWNSTREAM_FANOUT",
				Delta:    fanoutScore,
				Reason:   fmt.Sprintf("Changes propagate downstream to %d package(s)", downstreamCount),
			})
		}
	}

	// File churn penalty
	if len(changedFiles) > 10 {
		score += 15
		factors = append(factors, RiskFactor{
			Category: "HIGH_CHURN",
			Delta:    15,
			Reason:   fmt.Sprintf("High blast radius: %d files touched across monorepo", len(changedFiles)),
		})
	}

	// Cap score at 100
	if score > 100 {
		score = 100
	}

	// Determine Tier
	var tier RiskTier
	var verdict string

	if score <= 20 {
		tier = Tier1AutoApprovable
		verdict = "Low Risk: Eligible for automated merge train or fast-track review"
	} else if score <= 65 {
		tier = Tier2HumanReviewRequired
		verdict = "Medium Risk: Human reviewer attention required (business logic modified)"
	} else {
		tier = Tier3ElevatedRiskBlocked
		verdict = "High Risk: Elevated caution — touches critical schema, security, or infra"
	}

	return &RiskAssessment{
		Score:                 score,
		Tier:                  tier,
		Factors:               factors,
		CriticalFiles:         criticalFiles,
		DownstreamImpactCount: downstreamCount,
		TotalFilesChanged:     len(changedFiles),
		Verdict:               verdict,
	}, nil
}

// AuditPRDossier audits PR description claims against actual git diff to stop hallucinated PR slop.
func AuditPRDossier(prTitle, prDescription string, changedFiles []string) *AuditResult {
	res := &AuditResult{
		Valid: true,
	}

	combinedText := strings.ToLower(prTitle + "\n" + prDescription)

	// Check for title downplaying reality (e.g. "typo fix" or "doc update" with code changes)
	isClaimedTrivial := strings.Contains(combinedText, "typo") ||
		strings.Contains(combinedText, "doc update") ||
		strings.Contains(combinedText, "formatting only") ||
		strings.Contains(combinedText, "readme")

	for _, f := range changedFiles {
		if !docOnlyPattern.MatchString(f) && isClaimedTrivial {
			res.Valid = false
			res.HallucinationWarnings = append(res.HallucinationWarnings,
				fmt.Sprintf("PR claims trivial change ('%s') but modifies non-doc file: %s", prTitle, f))
		}
	}

	// Check if critical files touched are undisclosed in the description
	for _, f := range changedFiles {
		baseName := strings.ToLower(filepath.Base(f))
		dirName := strings.ToLower(filepath.Dir(f))

		isMentioned := strings.Contains(combinedText, baseName) ||
			strings.Contains(combinedText, dirName) ||
			strings.Contains(combinedText, strings.ToLower(f))

		// If it's a critical file (migration, security, infra), it MUST be explicitly mentioned
		isCritical := migrationPattern.MatchString(f) ||
			securityPattern.MatchString(f) ||
			infraPattern.MatchString(f) ||
			secretPattern.MatchString(f)

		if isCritical && !isMentioned {
			res.Valid = false
			res.UndeclaredFiles = append(res.UndeclaredFiles, f)
			res.HallucinationWarnings = append(res.HallucinationWarnings,
				fmt.Sprintf("Undisclosed critical blast radius: %s modified in Git but omitted from PR description", f))
		}
	}

	if res.Valid {
		res.Verdict = "PR claims faithfully reflect Git diff changes (zero hallucination detected)"
	} else {
		res.Verdict = "PR intent audit failed: unmentioned critical files or misleading claims detected"
	}

	return res
}

// CheckBackpressure evaluates whether the human review queue is saturated.
func CheckBackpressure(rootDir string, maxPRs int) (*BackpressureStatus, error) {
	if maxPRs <= 0 {
		maxPRs = 3 // Default backpressure threshold
	}

	queueDir := filepath.Join(rootDir, ".agents", "queue")
	_ = os.MkdirAll(queueDir, 0755)

	entries, err := os.ReadDir(queueDir)
	if err != nil {
		return nil, err
	}

	activeCount := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && strings.HasPrefix(e.Name(), "pr_") {
			activeCount++
		}
	}

	if activeCount >= maxPRs {
		return &BackpressureStatus{
			Throttled:         true,
			ActivePRCount:     activeCount,
			MaxAllowedPRs:     maxPRs,
			Reason:            fmt.Sprintf("Review capacity saturated: %d unreviewed agent PRs currently open (limit: %d)", activeCount, maxPRs),
			RecommendedAction: "Pause PR submission until human reviewers review or merge existing PRs to prevent decision fatigue.",
		}, nil
	}

	return &BackpressureStatus{
		Throttled:         false,
		ActivePRCount:     activeCount,
		MaxAllowedPRs:     maxPRs,
		Reason:            fmt.Sprintf("Queue healthy: %d/%d active PRs", activeCount, maxPRs),
		RecommendedAction: "Proceed with PR submission",
	}, nil
}

// RecordActivePR records a pending PR in the local queue for backpressure tracking.
func RecordActivePR(rootDir, prID, title string, score int, tier RiskTier) error {
	queueDir := filepath.Join(rootDir, ".agents", "queue")
	if err := os.MkdirAll(queueDir, 0755); err != nil {
		return err
	}

	filePath := filepath.Join(queueDir, fmt.Sprintf("pr_%s.json", prID))
	payload := map[string]any{
		"pr_id":      prID,
		"title":      title,
		"risk_score": score,
		"risk_tier":  string(tier),
		"status":     "open_pending_review",
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

// ClearActivePR removes a completed or merged PR from the backpressure queue.
func ClearActivePR(rootDir, prID string) error {
	filePath := filepath.Join(rootDir, ".agents", "queue", fmt.Sprintf("pr_%s.json", prID))
	return os.Remove(filePath)
}
