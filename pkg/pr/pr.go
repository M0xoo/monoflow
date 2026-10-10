package pr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/events"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
	"github.com/antigravity/monoflow/pkg/proof"
	"github.com/antigravity/monoflow/pkg/queue"
	"github.com/antigravity/monoflow/pkg/review"
	"github.com/antigravity/monoflow/pkg/subset"
	"github.com/antigravity/monoflow/pkg/task"
	"github.com/antigravity/monoflow/pkg/verify"
	"github.com/antigravity/monoflow/pkg/worktree"
)

// PRState defines the lifecycle state of a pull request.
type PRState string

const (
	StateOpen   PRState = "OPEN"
	StateMerged PRState = "MERGED"
	StateClosed PRState = "CLOSED"
	StateDraft  PRState = "DRAFT"
)

// PRMetadata encapsulates the complete agent-native PR dossier.
type PRMetadata struct {
	ID               string                 `json:"id"`
	GitHubNumber     int                    `json:"github_number,omitempty"`
	GitHubURL        string                 `json:"github_url,omitempty"`
	Title            string                 `json:"title"`
	Body             string                 `json:"body"`
	HeadBranch       string                 `json:"head_branch"`
	BaseBranch       string                 `json:"base_branch"`
	Author           string                 `json:"author"`
	State            PRState                `json:"state"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	HeadSHA          string                 `json:"head_sha"`
	BaseSHA          string                 `json:"base_sha"`
	AffectedPackages []string               `json:"affected_packages"`
	Risk             *review.RiskAssessment `json:"risk,omitempty"`
	Proof            *proof.ProofReceipt    `json:"proof,omitempty"`
	Audit            *review.AuditResult    `json:"audit,omitempty"`
	IsStacked        bool                   `json:"is_stacked,omitempty"`
	StackTiers       *task.StackedSlice     `json:"stack_tiers,omitempty"`
	ReviewDecision   string                 `json:"review_decision,omitempty"`
	Pushed           bool                   `json:"pushed,omitempty"`
	CIWatcher        string                 `json:"ci_watcher,omitempty"`
	CIWaitCommand    string                 `json:"ci_wait_command,omitempty"`
	RemotePR         *RemotePRResult        `json:"remote_pr,omitempty"`
}

// RemotePRResult reports how (or why not) the hosted PR was opened, so an agent in an
// environment without an authenticated gh (cloud sandboxes) knows exactly what to do next.
type RemotePRResult struct {
	Status     string `json:"status"`        // "created", "not_created", "skipped"
	Via        string `json:"via,omitempty"` // "gh", "api"
	Reason     string `json:"reason,omitempty"`
	CompareURL string `json:"compare_url,omitempty"`
	Directive  string `json:"agent_directive,omitempty"`
}

// CreateOptions controls PR generation.
type CreateOptions struct {
	Title      string
	Body       string
	HeadBranch string
	BaseBranch string
	Draft      bool
	Stack      bool
	Remote     string
	AutoPush   bool
}

// ListOptions controls PR filtering.
type ListOptions struct {
	State string // "open", "closed", "merged", "all"
	Limit int
}

// ReviewOptions specifies machine or human sign-off actions.
type ReviewOptions struct {
	Approve        bool
	RequestChanges bool
	Comment        string
}

// PRReviewResult records the review outcome.
type PRReviewResult struct {
	PRID           string                 `json:"pr_id"`
	Status         string                 `json:"status"`
	ReviewDecision string                 `json:"review_decision"`
	Comment        string                 `json:"comment,omitempty"`
	Risk           *review.RiskAssessment `json:"risk,omitempty"`
	Audit          *review.AuditResult    `json:"audit,omitempty"`
	DurationMs     float64                `json:"duration_ms"`
}

// TestOptions configures delta hot-mount testing.
type TestOptions struct {
	AgainstBase bool
	BaseBranch  string
}

// PRTestResult records hot-mount delta verification.
type PRTestResult struct {
	PRID               string   `json:"pr_id"`
	Status             string   `json:"status"` // "passed" or "failed"
	BaseBranch         string   `json:"base_branch"`
	MountMode          string   `json:"mount_mode"`
	CompiledPackages   []string `json:"compiled_packages"`
	UnchangedBasePkgs  int      `json:"unchanged_base_pkgs"`
	VerificationTimeMs float64  `json:"verification_time_ms"`
	ConfidenceScore    float64  `json:"confidence_score"`
	Verdict            string   `json:"verdict"`
}

// MergeOptions defines PR merge parameters.
type MergeOptions struct {
	Strategy     string // "squash", "merge", "rebase"
	UseQueue     bool
	DeleteBranch bool
}

// PRMergeResult details the merge operation.
type PRMergeResult struct {
	PRID       string  `json:"pr_id"`
	Status     string  `json:"status"`
	Strategy   string  `json:"strategy"`
	CommitSHA  string  `json:"commit_sha,omitempty"`
	DurationMs float64 `json:"duration_ms"`
	QueueJob   any     `json:"queue_job,omitempty"`
}

// LocalOptions configures local projection of a PR's affected packages.
type LocalOptions struct {
	WorktreeID  string             `json:"worktree_id,omitempty"`
	DepStrategy subset.DepStrategy `json:"dep_strategy,omitempty"`
	Remote      string             `json:"remote,omitempty"`
}

// PRLocalResult describes the local materialization of the PR's affected packages.
type PRLocalResult struct {
	PRID                 string   `json:"pr_id"`
	Status               string   `json:"status"` // "projected" or "worktree_created"
	Mode                 string   `json:"mode"`   // "workspace" or "worktree"
	HeadBranch           string   `json:"head_branch"`
	AffectedPackages     []string `json:"affected_packages"`
	MaterializedPackages []string `json:"materialized_packages"`
	DepStrategy          string   `json:"dep_strategy"`
	WorkingDir           string   `json:"working_dir"`
	SavingsPct           float64  `json:"savings_pct"`
	DurationMs           float64  `json:"duration_ms"`
}

// PRManager coordinates PR creation, testing, machine review, and merging.
type PRManager struct {
	mu      sync.Mutex
	RootDir string
	prsDir  string
}

// NewManager creates an instance of PRManager.
func NewManager(rootDir string) *PRManager {
	dir := resolvePRDir(rootDir)
	return &PRManager{
		RootDir: rootDir,
		prsDir:  dir,
	}
}

func resolvePRDir(rootDir string) string {
	dir := filepath.Join(rootDir, ".agents", "prs")
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return dir
	}
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = rootDir
	if out, err := cmd.CombinedOutput(); err == nil {
		commonDir := strings.TrimSpace(string(out))
		if commonDir != "" {
			mainRoot := filepath.Dir(commonDir)
			candidate := filepath.Join(mainRoot, ".agents", "prs")
			if entries, err := os.ReadDir(candidate); err == nil && len(entries) > 0 {
				return candidate
			}
		}
	}
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// Create generates a complete PR with machine risk score, proof of work, and GitHub integration.
func (m *PRManager) Create(opts CreateOptions) (*PRMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	start := time.Now()

	// 1. Resolve branches
	cmdCurrent := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmdCurrent.Dir = m.RootDir
	currentBranch := "main"
	if out, err := cmdCurrent.CombinedOutput(); err == nil {
		currentBranch = strings.TrimSpace(string(out))
	}

	baseBranch := opts.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	headBranch := opts.HeadBranch
	if headBranch == "" {
		if currentBranch != baseBranch && currentBranch != "HEAD" {
			headBranch = currentBranch
		} else {
			headBranch = fmt.Sprintf("feat/pr-%d", time.Now().Unix())
		}
	}

	// Never switch branches: the checkout may be shared (repo root) or managed by the
	// environment (cloud sandboxes). If the head branch differs from the current one,
	// point a new branch at HEAD without checking it out; refuse to move an existing one.
	if currentBranch != headBranch {
		if err := m.ensureBranchAtHEAD(headBranch); err != nil {
			return nil, err
		}
	}

	if headBranch == baseBranch {
		return nil, fmt.Errorf("cannot create PR from base branch '%s'; please switch to or create a feature branch first", baseBranch)
	}

	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}

	// 2. Resolve commits and changed files
	cmdHeadSHA := exec.Command("git", "rev-parse", "HEAD")
	cmdHeadSHA.Dir = m.RootDir
	headSHABytes, _ := cmdHeadSHA.CombinedOutput()
	headSHA := strings.TrimSpace(string(headSHABytes))

	cmdBaseSHA := exec.Command("git", "rev-parse", baseBranch)
	cmdBaseSHA.Dir = m.RootDir
	baseSHABytes, _ := cmdBaseSHA.CombinedOutput()
	baseSHA := strings.TrimSpace(string(baseSHABytes))

	changedFiles, err := m.getDiffFiles(baseBranch, headBranch)
	if err != nil || len(changedFiles) == 0 {
		return nil, fmt.Errorf("no changed files detected between '%s' and '%s'", baseBranch, headBranch)
	}

	// 3. Pre-flight Guard Check: Secrets & Test Integrity
	testViolations, _ := guard.CheckTestIntegrity(m.RootDir)
	secretViolations, _ := guard.ScanSecrets(m.RootDir)
	guardViolations := append(testViolations, secretViolations...)
	if len(guardViolations) > 0 {
		return nil, fmt.Errorf("PR creation blocked by MonoFlow Guard: %d security/integrity violation(s) detected", len(guardViolations))
	}

	// 4. Dependency blast radius & affected packages
	g, _ := graph.LoadOrDiscover(m.RootDir)
	var affected []string
	if g != nil {
		affected = g.ComputeAffected(changedFiles).AllAffected
	}

	// 5. Evaluate Machine Risk
	risk, err := review.EvaluateRisk(m.RootDir, g, changedFiles)
	if err != nil {
		risk = &review.RiskAssessment{
			Score:   10,
			Tier:    review.Tier1AutoApprovable,
			Verdict: "Auto-assessed baseline risk",
		}
	}

	// 6. Audit PR Claims for hallucinations
	if opts.Title == "" {
		opts.Title = fmt.Sprintf("feat(%s): agent changes on %s", strings.ReplaceAll(headBranch, "/", "-"), headBranch)
	}
	audit := review.AuditPRDossier(opts.Title, opts.Body, changedFiles)

	// 7. Generate DoD & Proof of Work Receipt
	proofReceipt, _, _ := proof.ProveTask(m.RootDir, headBranch, g, changedFiles)

	// 7b. Strict PR Gate Barrier: never push or open a PR that would fail remote CI.
	// Drafts are exempt so agents can still share work-in-progress explicitly.
	if !opts.Draft {
		if gateErr := checkPRGate(proofReceipt, audit); gateErr != nil {
			return nil, gateErr
		}
	}

	// 8. Stacked Slicing if requested
	var stackTiers *task.StackedSlice
	if opts.Stack {
		sliced := task.SliceStackedDiff(changedFiles)
		stackTiers = &sliced
	}

	// 9. Format Machine PR Body
	fullBody := m.buildPRMarkdownBody(opts.Title, opts.Body, risk, proofReceipt, audit, affected, stackTiers)

	// 10. Auto-push head branch if requested
	pushed := false
	var pushErr string
	if opts.AutoPush {
		cmdPush := exec.Command("git", "push", "-u", remote, headBranch+":"+headBranch)
		cmdPush.Dir = m.RootDir
		if out, err := cmdPush.CombinedOutput(); err == nil {
			pushed = true
		} else {
			pushErr = strings.TrimSpace(string(out))
		}
	}

	// 11. Create Pull Request via GitHub CLI (gh) if available
	ghNumber := 0
	ghURL := ""
	state := StateOpen
	if opts.Draft {
		state = StateDraft
	}

	remotePR := m.openRemotePR(opts, fullBody, baseBranch, headBranch, pushed || !opts.AutoPush, pushErr)
	if remotePR.Status == "created" {
		ghURL = remotePR.CompareURL
		remotePR.CompareURL = ""
		if matches := regexp.MustCompile(`/pull/(\d+)`).FindStringSubmatch(ghURL); len(matches) > 1 {
			ghNumber, _ = strconv.Atoi(matches[1])
		}
	}

	// 12. Create PR Metadata
	id := fmt.Sprintf("pr-%d", ghNumber)
	if ghNumber == 0 {
		id = fmt.Sprintf("pr-%s-%d", strings.ReplaceAll(headBranch, "/", "-"), time.Now().Unix())
	}

	author := "agent"
	cmdUser := exec.Command("git", "config", "user.name")
	cmdUser.Dir = m.RootDir
	if uOut, uErr := cmdUser.CombinedOutput(); uErr == nil && len(strings.TrimSpace(string(uOut))) > 0 {
		author = strings.TrimSpace(string(uOut))
	}

	metadata := &PRMetadata{
		ID:               id,
		GitHubNumber:     ghNumber,
		GitHubURL:        ghURL,
		Title:            opts.Title,
		Body:             fullBody,
		HeadBranch:       headBranch,
		BaseBranch:       baseBranch,
		Author:           author,
		State:            state,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
		HeadSHA:          headSHA,
		BaseSHA:          baseSHA,
		AffectedPackages: affected,
		Risk:             risk,
		Proof:            proofReceipt,
		Audit:            audit,
		IsStacked:        opts.Stack,
		StackTiers:       stackTiers,
		Pushed:           pushed,
		RemotePR:         remotePR,
	}
	// The PR push is the CI trigger: watch it here so agents need no separate `push`.
	if pushed {
		if job, err := events.NewCIWatcher(m.RootDir).StartWatch(events.WatchOptions{CommitSHA: headSHA, Branch: headBranch, Remote: remote, Async: true}); err == nil {
			metadata.CIWatcher = job.ID
			metadata.CIWaitCommand = "./bin/monoflow events wait " + headSHA + "   # run as a background task, then end your turn"
		}
	}

	if err := m.save(metadata); err != nil {
		return nil, fmt.Errorf("failed to persist PR metadata: %w", err)
	}

	// 13. Emit Event on Bus
	evMgr := events.NewEventManager(m.RootDir)
	_ = evMgr.Emit(&events.Event{
		ID:             fmt.Sprintf("evt_pr_created_%s", id),
		Type:           events.EventPRCreated,
		CommitSHA:      headSHA,
		Branch:         headBranch,
		CreatedAt:      time.Now().UTC(),
		RequiresAction: risk.Tier != review.Tier1AutoApprovable,
		Summary:        fmt.Sprintf("Pull Request %s created: %s [%s]", id, opts.Title, risk.Tier),
		Payload: map[string]any{
			"pr_id":             id,
			"github_url":        ghURL,
			"risk_tier":         risk.Tier,
			"risk_score":        risk.Score,
			"affected_packages": affected,
			"duration_ms":       float64(time.Since(start).Microseconds()) / 1000.0,
		},
	})

	return metadata, nil
}

// GateError reports that a PR was blocked locally because its DoD proof or audit failed.
type GateError struct {
	ProofStatus  string              `json:"proof_status"`
	FailedChecks []proof.DoDItem     `json:"failed_checks,omitempty"`
	Audit        *review.AuditResult `json:"audit,omitempty"`
}

func (e *GateError) Error() string {
	var reasons []string
	if e.ProofStatus != "VERIFIED" {
		var ids []string
		for _, c := range e.FailedChecks {
			ids = append(ids, c.ID)
		}
		reasons = append(reasons, fmt.Sprintf("DoD proof %s (failed: %s)", e.ProofStatus, strings.Join(ids, ", ")))
	}
	if e.Audit != nil && !e.Audit.Valid {
		reasons = append(reasons, fmt.Sprintf("PR audit invalid (%d warning(s))", len(e.Audit.HallucinationWarnings)))
	}
	return fmt.Sprintf("PR creation blocked by MonoFlow PR Gate: %s; fix and re-run, or pass --draft", strings.Join(reasons, "; "))
}

// checkPRGate returns a *GateError unless the proof is VERIFIED and the audit is valid.
func checkPRGate(p *proof.ProofReceipt, audit *review.AuditResult) error {
	status := "MISSING"
	var failed []proof.DoDItem
	if p != nil {
		status = p.Status
		for _, c := range p.DoDChecks {
			if c.Status != "PASSED" {
				failed = append(failed, c)
			}
		}
	}
	if status == "VERIFIED" && (audit == nil || audit.Valid) {
		return nil
	}
	return &GateError{ProofStatus: status, FailedChecks: failed, Audit: audit}
}

// List returns recorded PRs, optionally merged with GitHub CLI data.
func (m *PRManager) List(opts ListOptions) ([]PRMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	localPRs := make(map[string]PRMetadata)
	entries, _ := os.ReadDir(m.prsDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			data, err := os.ReadFile(filepath.Join(m.prsDir, entry.Name()))
			if err == nil {
				var pr PRMetadata
				if json.Unmarshal(data, &pr) == nil {
					localPRs[pr.ID] = pr
					if pr.GitHubNumber > 0 {
						localPRs[fmt.Sprintf("%d", pr.GitHubNumber)] = pr
					}
				}
			}
		}
	}

	// Enrich from GitHub CLI if installed
	if _, lookErr := exec.LookPath("gh"); lookErr == nil {
		cmdList := exec.Command("gh", "pr", "list", "--json", "number,title,headRefName,baseRefName,state,url,author,isDraft,createdAt")
		cmdList.Dir = m.RootDir
		if out, err := cmdList.CombinedOutput(); err == nil {
			var ghPRs []struct {
				Number      int    `json:"number"`
				Title       string `json:"title"`
				HeadRefName string `json:"headRefName"`
				BaseRefName string `json:"baseRefName"`
				State       string `json:"state"`
				URL         string `json:"url"`
				IsDraft     bool   `json:"isDraft"`
				Author      struct {
					Login string `json:"login"`
				} `json:"author"`
			}
			if json.Unmarshal(out, &ghPRs) == nil {
				for _, gpr := range ghPRs {
					key := fmt.Sprintf("%d", gpr.Number)
					existing, exists := localPRs[key]
					if exists {
						existing.GitHubURL = gpr.URL
						existing.GitHubNumber = gpr.Number
						if gpr.State == "MERGED" {
							existing.State = StateMerged
						} else if gpr.State == "CLOSED" {
							existing.State = StateClosed
						}
						localPRs[key] = existing
					} else {
						st := StateOpen
						if gpr.IsDraft {
							st = StateDraft
						} else if gpr.State == "MERGED" {
							st = StateMerged
						} else if gpr.State == "CLOSED" {
							st = StateClosed
						}
						localPRs[key] = PRMetadata{
							ID:           fmt.Sprintf("pr-%d", gpr.Number),
							GitHubNumber: gpr.Number,
							GitHubURL:    gpr.URL,
							Title:        gpr.Title,
							HeadBranch:   gpr.HeadRefName,
							BaseBranch:   gpr.BaseRefName,
							Author:       gpr.Author.Login,
							State:        st,
						}
					}
				}
			}
		}
	}

	// Consolidate unique PRs
	seen := make(map[string]bool)
	var result []PRMetadata
	for _, pr := range localPRs {
		if seen[pr.ID] {
			continue
		}
		seen[pr.ID] = true

		if opts.State != "" && opts.State != "all" {
			if strings.ToUpper(opts.State) != string(pr.State) {
				continue
			}
		}
		result = append(result, pr)
	}

	// Sort newest first
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}

	return result, nil
}

// View retrieves metadata for a single PR.
func (m *PRManager) View(id string) (*PRMetadata, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check local records
	cleanID := strings.TrimPrefix(id, "#")
	entries, _ := os.ReadDir(m.prsDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			data, err := os.ReadFile(filepath.Join(m.prsDir, entry.Name()))
			if err == nil {
				var pr PRMetadata
				if json.Unmarshal(data, &pr) == nil {
					if pr.ID == cleanID || pr.ID == "pr-"+cleanID || fmt.Sprintf("%d", pr.GitHubNumber) == cleanID {
						return &pr, nil
					}
				}
			}
		}
	}

	// If not found locally, query GitHub CLI
	if _, lookErr := exec.LookPath("gh"); lookErr == nil {
		cmdView := exec.Command("gh", "pr", "view", cleanID, "--json", "number,title,body,headRefName,baseRefName,state,url,author,headRefOid,baseRefOid")
		cmdView.Dir = m.RootDir
		if out, err := cmdView.CombinedOutput(); err == nil {
			var ghPR struct {
				Number      int    `json:"number"`
				Title       string `json:"title"`
				Body        string `json:"body"`
				HeadRefName string `json:"headRefName"`
				BaseRefName string `json:"baseRefName"`
				HeadRefOid  string `json:"headRefOid"`
				BaseRefOid  string `json:"baseRefOid"`
				State       string `json:"state"`
				URL         string `json:"url"`
				Author      struct {
					Login string `json:"login"`
				} `json:"author"`
			}
			if json.Unmarshal(out, &ghPR) == nil {
				return &PRMetadata{
					ID:           fmt.Sprintf("pr-%d", ghPR.Number),
					GitHubNumber: ghPR.Number,
					GitHubURL:    ghPR.URL,
					Title:        ghPR.Title,
					Body:         ghPR.Body,
					HeadBranch:   ghPR.HeadRefName,
					BaseBranch:   ghPR.BaseRefName,
					HeadSHA:      ghPR.HeadRefOid,
					BaseSHA:      ghPR.BaseRefOid,
					Author:       ghPR.Author.Login,
					State:        PRState(ghPR.State),
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("pull request '%s' not found", id)
}

// Review conducts a machine review evaluation, optionally executing GitHub approval.
func (m *PRManager) Review(id string, opts ReviewOptions) (*PRReviewResult, error) {
	start := time.Now()
	pr, err := m.View(id)
	if err != nil {
		return nil, err
	}

	decision := "REVIEWED"
	comment := opts.Comment

	if opts.Approve {
		decision = "APPROVED"
		if comment == "" {
			comment = fmt.Sprintf("MonoFlow Machine Review: Approved. Risk score: %d/100 (%s). DoD verified.",
				pr.Risk.Score, pr.Risk.Tier)
		}

		if _, lookErr := exec.LookPath("gh"); lookErr == nil && pr.GitHubNumber > 0 {
			cmdReview := exec.Command("gh", "pr", "review", fmt.Sprintf("%d", pr.GitHubNumber), "--approve", "-b", comment)
			cmdReview.Dir = m.RootDir
			_ = cmdReview.Run()
		}
	} else if opts.RequestChanges {
		decision = "CHANGES_REQUESTED"
		if comment == "" {
			comment = "MonoFlow Machine Review: Changes requested due to integrity, secret, or blast-radius policy violations."
		}

		if _, lookErr := exec.LookPath("gh"); lookErr == nil && pr.GitHubNumber > 0 {
			cmdReview := exec.Command("gh", "pr", "review", fmt.Sprintf("%d", pr.GitHubNumber), "-r", "-b", comment)
			cmdReview.Dir = m.RootDir
			_ = cmdReview.Run()
		}
	} else if comment != "" {
		decision = "COMMENTED"
		if _, lookErr := exec.LookPath("gh"); lookErr == nil && pr.GitHubNumber > 0 {
			cmdReview := exec.Command("gh", "pr", "review", fmt.Sprintf("%d", pr.GitHubNumber), "-c", "-b", comment)
			cmdReview.Dir = m.RootDir
			_ = cmdReview.Run()
		}
	}

	pr.ReviewDecision = decision
	pr.UpdatedAt = time.Now().UTC()
	_ = m.save(pr)

	// Emit Event
	evMgr := events.NewEventManager(m.RootDir)
	_ = evMgr.Emit(&events.Event{
		ID:        fmt.Sprintf("evt_pr_reviewed_%s", pr.ID),
		Type:      events.EventPRReviewed,
		CommitSHA: pr.HeadSHA,
		Branch:    pr.HeadBranch,
		CreatedAt: time.Now().UTC(),
		Summary:   fmt.Sprintf("PR %s reviewed: %s", pr.ID, decision),
		Payload: map[string]any{
			"pr_id":           pr.ID,
			"review_decision": decision,
			"comment":         comment,
		},
	})

	return &PRReviewResult{
		PRID:           pr.ID,
		Status:         "success",
		ReviewDecision: decision,
		Comment:        comment,
		Risk:           pr.Risk,
		Audit:          pr.Audit,
		DurationMs:     float64(time.Since(start).Microseconds()) / 1000.0,
	}, nil
}

// Test hot-mounts the PR delta against the base branch and verifies compilation/tests in < 3s.
func (m *PRManager) Test(id string, opts TestOptions) (*PRTestResult, error) {
	start := time.Now()
	pr, err := m.View(id)
	if err != nil {
		return nil, err
	}

	baseBranch := opts.BaseBranch
	if baseBranch == "" {
		baseBranch = pr.BaseBranch
		if baseBranch == "" {
			baseBranch = "main"
		}
	}

	// 1. Determine affected packages
	changedFiles, err := m.getDiffFiles(baseBranch, pr.HeadBranch)
	if err != nil || len(changedFiles) == 0 {
		changedFiles = pr.AffectedPackages
	}

	g, _ := graph.LoadOrDiscover(m.RootDir)
	var affected []string
	if g != nil {
		affected = g.ComputeAffected(changedFiles).AllAffected
	}
	if len(affected) == 0 {
		affected = pr.AffectedPackages
	}
	if len(affected) == 0 && g != nil {
		for _, p := range g.GetAllPackages() {
			affected = append(affected, p.Name)
			break
		}
	}

	// 2. Execute Inner Loop Verification on affected delta
	runner := verify.NewRunner(m.RootDir)
	vRes := runner.VerifyAffected(g, changedFiles, verify.VerifyOptions{})
	durMs := float64(time.Since(start).Microseconds()) / 1000.0

	status := "passed"
	verdict := fmt.Sprintf("PR Delta hot-mounted on top of latest %s passed all verification in %.2fs", baseBranch, durMs/1000.0)
	if vRes.Status != "passed" {
		status = "failed"
		verdict = fmt.Sprintf("PR Delta failed verification against %s in %.2fs", baseBranch, durMs/1000.0)
	}

	// 3. Concurrency data race detector gate
	if _, err := os.Stat(filepath.Join(m.RootDir, "go.mod")); err == nil {
		var goPkgs []string
		if _, err := os.Stat(filepath.Join(m.RootDir, "pkg")); err == nil {
			goPkgs = append(goPkgs, "./pkg/...")
		} else if g != nil && len(affected) > 0 {
			for _, pkgName := range affected {
				if pkgMeta, ok := g.GetPackage(pkgName); ok && pkgMeta != nil && pkgMeta.Language == "go" {
					goPkgs = append(goPkgs, "./"+pkgMeta.Path+"/...")
				}
			}
		}
		if len(goPkgs) > 0 {
			args := append([]string{"test", "-race", "-short"}, goPkgs...)
			cmdRace := exec.Command("go", args...)
			cmdRace.Dir = m.RootDir
			if raceOut, raceErr := cmdRace.CombinedOutput(); raceErr != nil {
				status = "failed"
				verdict = fmt.Sprintf("PR Delta failed: data race detected: %s", strings.TrimSpace(string(raceOut)))
			}
		}
	}

	totalCount := 0
	if g != nil {
		totalCount = len(g.GetAllPackages())
	}
	unchanged := totalCount - len(affected)
	if unchanged < 0 {
		unchanged = 0
	}

	// Emit Event
	evMgr := events.NewEventManager(m.RootDir)
	_ = evMgr.Emit(&events.Event{
		ID:        fmt.Sprintf("evt_pr_tested_%s", pr.ID),
		Type:      events.EventPRTested,
		CommitSHA: pr.HeadSHA,
		Branch:    pr.HeadBranch,
		CreatedAt: time.Now().UTC(),
		Summary:   fmt.Sprintf("PR %s tested against %s: %s", pr.ID, baseBranch, status),
		Payload: map[string]any{
			"pr_id":             pr.ID,
			"status":            status,
			"compiled_packages": affected,
			"duration_ms":       durMs,
		},
	})

	return &PRTestResult{
		PRID:               pr.ID,
		Status:             status,
		BaseBranch:         baseBranch,
		MountMode:          "delta-overlay",
		CompiledPackages:   affected,
		UnchangedBasePkgs:  unchanged,
		VerificationTimeMs: durMs,
		ConfidenceScore:    0.99,
		Verdict:            verdict,
	}, nil
}

// Merge promotes the PR either via speculative merge queue or direct GitHub merge.
func (m *PRManager) Merge(id string, opts MergeOptions) (*PRMergeResult, error) {
	start := time.Now()
	pr, err := m.View(id)
	if err != nil {
		return nil, err
	}

	strategy := opts.Strategy
	if strategy == "" {
		strategy = "squash"
	}

	// 0. Pre-merge gate: verify PR before merging
	tRes, tErr := m.Test(id, TestOptions{BaseBranch: pr.BaseBranch})
	if tErr != nil || (tRes != nil && tRes.Status != "passed") {
		verdict := "verification failed"
		if tRes != nil {
			verdict = tRes.Verdict
		}
		return nil, fmt.Errorf("cannot merge PR '%s': pre-merge gate blocked (%s)", id, verdict)
	}

	// If speculative queue requested
	if opts.UseQueue {
		qm := queue.NewQueueManager(m.RootDir)
		g, _ := graph.LoadOrDiscover(m.RootDir)
		changedFiles, _ := m.getDiffFiles(pr.BaseBranch, pr.HeadBranch)
		receipt, qErr := qm.SubmitSpeculative(pr.ID, g, changedFiles, queue.SpeculativeOptions{
			TargetBranch: pr.BaseBranch,
		})
		if qErr != nil {
			return nil, fmt.Errorf("failed to enqueue PR to speculative merge queue: %w", qErr)
		}

		pr.State = StateMerged
		pr.UpdatedAt = time.Now().UTC()
		_ = m.save(pr)

		return &PRMergeResult{
			PRID:       pr.ID,
			Status:     "enqueued_speculative",
			Strategy:   strategy,
			QueueJob:   receipt,
			DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
		}, nil
	}

	// GitHub CLI merge if PR number is available
	if _, lookErr := exec.LookPath("gh"); lookErr == nil && pr.GitHubNumber > 0 {
		args := []string{"pr", "merge", fmt.Sprintf("%d", pr.GitHubNumber), "--" + strategy}
		if opts.DeleteBranch {
			args = append(args, "--delete-branch")
		}
		cmdMerge := exec.Command("gh", args...)
		cmdMerge.Dir = m.RootDir
		out, mErr := cmdMerge.CombinedOutput()
		if mErr != nil {
			return nil, fmt.Errorf("gh pr merge failed: %w (%s)", mErr, string(out))
		}
	} else {
		// Git local squash/merge fallback
		cmdCheckout := exec.Command("git", "checkout", pr.BaseBranch)
		cmdCheckout.Dir = m.RootDir
		if out, err := cmdCheckout.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("failed to checkout base branch '%s': %w (%s)", pr.BaseBranch, err, string(out))
		}

		mergeArg := "--squash"
		if strategy == "merge" {
			mergeArg = "--no-ff"
		}
		cmdMerge := exec.Command("git", "merge", mergeArg, pr.HeadBranch)
		cmdMerge.Dir = m.RootDir
		if out, err := cmdMerge.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git merge failed: %w (%s)", err, string(out))
		}

		msg := fmt.Sprintf("Merge PR #%s: %s", pr.ID, pr.Title)
		cmdCommit := exec.Command("git", "commit", "--no-verify", "-m", msg)
		cmdCommit.Dir = m.RootDir
		_ = cmdCommit.Run()

		if opts.DeleteBranch && pr.HeadBranch != pr.BaseBranch {
			cmdDel := exec.Command("git", "branch", "-D", pr.HeadBranch)
			cmdDel.Dir = m.RootDir
			_ = cmdDel.Run()
		}
	}

	cmdSHA := exec.Command("git", "rev-parse", "HEAD")
	cmdSHA.Dir = m.RootDir
	shaOut, _ := cmdSHA.CombinedOutput()
	sha := strings.TrimSpace(string(shaOut))

	pr.State = StateMerged
	pr.UpdatedAt = time.Now().UTC()
	_ = m.save(pr)

	// Emit Event
	evMgr := events.NewEventManager(m.RootDir)
	_ = evMgr.Emit(&events.Event{
		ID:        fmt.Sprintf("evt_pr_merged_%s", pr.ID),
		Type:      events.EventPRMerged,
		CommitSHA: sha,
		Branch:    pr.BaseBranch,
		CreatedAt: time.Now().UTC(),
		Summary:   fmt.Sprintf("PR %s merged into %s via %s", pr.ID, pr.BaseBranch, strategy),
		Payload: map[string]any{
			"pr_id":      pr.ID,
			"strategy":   strategy,
			"commit_sha": sha,
		},
	})

	return &PRMergeResult{
		PRID:       pr.ID,
		Status:     "merged",
		Strategy:   strategy,
		CommitSHA:  sha,
		DurationMs: float64(time.Since(start).Microseconds()) / 1000.0,
	}, nil
}

// Local projects the workspace or creates a virtual worktree containing ONLY the PR's affected packages.
func (m *PRManager) Local(id string, opts LocalOptions) (*PRLocalResult, error) {
	start := time.Now()

	// 1. Retrieve or fetch PR
	pr, err := m.View(id)
	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}

	if err != nil {
		cleanID := strings.TrimPrefix(id, "#")
		fetchRef := fmt.Sprintf("pull/%s/head:pr-%s", cleanID, cleanID)
		cmdFetch := exec.Command("git", "fetch", remote, fetchRef)
		cmdFetch.Dir = m.RootDir
		if fOut, fErr := cmdFetch.CombinedOutput(); fErr != nil {
			cmdFetchBranch := exec.Command("git", "fetch", remote, id)
			cmdFetchBranch.Dir = m.RootDir
			if _, fbErr := cmdFetchBranch.CombinedOutput(); fbErr != nil {
				return nil, fmt.Errorf("could not view or fetch PR '%s': %w (%s)", id, fErr, string(fOut))
			}
		}

		targetRef := fmt.Sprintf("pr-%s", cleanID)
		cmdCheckRef := exec.Command("git", "rev-parse", "--verify", targetRef)
		cmdCheckRef.Dir = m.RootDir
		if err := cmdCheckRef.Run(); err != nil {
			targetRef = id
		}

		g, _ := graph.LoadOrDiscover(m.RootDir)
		diffFiles, _ := m.getDiffFiles("main", targetRef)
		var affected []string
		if g != nil {
			affected = g.ComputeAffected(diffFiles).AllAffected
		}

		pr = &PRMetadata{
			ID:               fmt.Sprintf("pr-%s", cleanID),
			HeadBranch:       targetRef,
			BaseBranch:       "main",
			AffectedPackages: affected,
		}
	}

	// 2. Resolve affected packages
	affected := pr.AffectedPackages
	if len(affected) == 0 {
		g, _ := graph.LoadOrDiscover(m.RootDir)
		diffFiles, _ := m.getDiffFiles(pr.BaseBranch, pr.HeadBranch)
		if g != nil {
			affected = g.ComputeAffected(diffFiles).AllAffected
		}
	}
	if len(affected) == 0 {
		g, _ := graph.LoadOrDiscover(m.RootDir)
		if g != nil {
			for _, p := range g.GetAllPackages() {
				affected = append(affected, p.Name)
			}
		}
	}

	strategy := opts.DepStrategy
	if strategy == "" {
		strategy = subset.DepStrategyContracts
	}

	// 3. Option A: Virtual Worktree isolation
	if opts.WorktreeID != "" {
		wtMgr := worktree.NewManager(m.RootDir)
		wtInfo, err := wtMgr.CreateWithOpts(opts.WorktreeID, worktree.CreateOptions{
			Packages: affected,
			Strategy: strategy,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create virtual worktree for PR '%s': %w", id, err)
		}

		// Checkout PR head in worktree if head branch is valid
		if pr.HeadBranch != "" {
			cmdCheckout := exec.Command("git", "checkout", pr.HeadBranch)
			cmdCheckout.Dir = wtInfo.Path
			_ = cmdCheckout.Run()
		}

		durMs := float64(time.Since(start).Microseconds()) / 1000.0
		totalPkgs := len(wtInfo.Packages) + len(wtInfo.OmittedPackages)
		savingsPct := 0.0
		if totalPkgs > 0 {
			savingsPct = float64(len(wtInfo.OmittedPackages)) / float64(totalPkgs) * 100.0
		}

		return &PRLocalResult{
			PRID:                 pr.ID,
			Status:               "worktree_created",
			Mode:                 "worktree",
			HeadBranch:           pr.HeadBranch,
			AffectedPackages:     affected,
			MaterializedPackages: wtInfo.Packages,
			DepStrategy:          string(strategy),
			WorkingDir:           wtInfo.Path,
			SavingsPct:           savingsPct,
			DurationMs:           durMs,
		}, nil
	}

	// 4. Option B: Workspace Projection (In-Place)
	chkMgr := checkpoint.NewManager(m.RootDir)
	_, _ = chkMgr.Save("pre-pr-local-" + pr.ID)

	cmdCur := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmdCur.Dir = m.RootDir
	curBranch := ""
	if out, err := cmdCur.CombinedOutput(); err == nil {
		curBranch = strings.TrimSpace(string(out))
	}

	if curBranch != pr.HeadBranch && pr.HeadBranch != "" {
		cmdCheckout := exec.Command("git", "checkout", pr.HeadBranch)
		cmdCheckout.Dir = m.RootDir
		_ = cmdCheckout.Run()
	}

	subRes, err := subset.ApplyWithOptions(m.RootDir, affected, strategy)
	if err != nil {
		return nil, fmt.Errorf("failed to apply local subset projection for PR '%s': %w", id, err)
	}

	durMs := float64(time.Since(start).Microseconds()) / 1000.0
	return &PRLocalResult{
		PRID:                 pr.ID,
		Status:               "projected",
		Mode:                 "workspace",
		HeadBranch:           pr.HeadBranch,
		AffectedPackages:     affected,
		MaterializedPackages: subRes.IncludedPackages,
		DepStrategy:          string(strategy),
		WorkingDir:           m.RootDir,
		SavingsPct:           subRes.DiskSavingsPct,
		DurationMs:           durMs,
	}, nil
}

func (m *PRManager) getDiffFiles(base, head string) ([]string, error) {
	// Try base...head diff
	cmd := exec.Command("git", "diff", "--name-only", base+"..."+head)
	cmd.Dir = m.RootDir
	out, err := cmd.CombinedOutput()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return splitLines(string(out)), nil
	}

	// Try origin/base...head diff
	cmdRemote := exec.Command("git", "diff", "--name-only", "origin/"+base+"..."+head)
	cmdRemote.Dir = m.RootDir
	outR, errR := cmdRemote.CombinedOutput()
	if errR == nil && len(strings.TrimSpace(string(outR))) > 0 {
		return splitLines(string(outR)), nil
	}

	// Fallback to local HEAD vs base
	cmdFallback := exec.Command("git", "diff", "--name-only", base)
	cmdFallback.Dir = m.RootDir
	outF, errF := cmdFallback.CombinedOutput()
	if errF == nil && len(strings.TrimSpace(string(outF))) > 0 {
		return splitLines(string(outF)), nil
	}

	return nil, fmt.Errorf("no diff detected")
}

func splitLines(s string) []string {
	var res []string
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func (m *PRManager) save(pr *PRMetadata) error {
	path := filepath.Join(m.prsDir, pr.ID+".json")
	data, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (m *PRManager) buildPRMarkdownBody(title, userBody string, risk *review.RiskAssessment, p *proof.ProofReceipt, audit *review.AuditResult, affected []string, stack *task.StackedSlice) string {
	var sb strings.Builder

	if userBody != "" {
		sb.WriteString(userBody)
		sb.WriteString("\n\n---\n\n")
	}

	sb.WriteString("## 🤖 MonoFlow Autonomous PR Dossier\n\n")

	// 1. Machine Risk Assessment
	badge := "🟢 Auto-Approvable (Tier 1)"
	if risk.Tier == review.Tier2HumanReviewRequired {
		badge = "🟡 Human Review Required (Tier 2)"
	} else if risk.Tier == review.Tier3ElevatedRiskBlocked {
		badge = "🔴 Elevated Caution Blocked (Tier 3)"
	}

	sb.WriteString(fmt.Sprintf("### 🛡️ Risk Assessment: %s\n", badge))
	sb.WriteString(fmt.Sprintf("- **Machine Risk Score**: `%d / 100`\n", risk.Score))
	sb.WriteString(fmt.Sprintf("- **Verdict**: %s\n", risk.Verdict))
	sb.WriteString(fmt.Sprintf("- **Downstream Blast Radius**: `%d` impacted package(s)\n", risk.DownstreamImpactCount))

	if len(risk.Factors) > 0 {
		sb.WriteString("\n**Risk Factors Identified**:\n")
		for _, f := range risk.Factors {
			sb.WriteString(fmt.Sprintf("- `+%d` **%s**: %s\n", f.Delta, f.Category, f.Reason))
		}
	}

	// 2. Anti-Hallucination Audit
	sb.WriteString("\n### 🔍 Intent & Scope Audit\n")
	if audit.Valid {
		sb.WriteString("- ✅ **Zero Hallucination Detected**: PR claims faithfully match git diff changes.\n")
	} else {
		sb.WriteString("- ⚠️ **Audit Warnings**:\n")
		for _, w := range audit.HallucinationWarnings {
			sb.WriteString(fmt.Sprintf("  - %s\n", w))
		}
	}

	// 3. Proof of Work Receipt
	if p != nil {
		sb.WriteString("\n### 🧾 Proof of Work Receipt\n")
		sb.WriteString(fmt.Sprintf("- **Status**: `%s` (verified in `%.2fms`)\n", p.Status, p.TotalDurationMs))
		sb.WriteString(fmt.Sprintf("- **Git Tree SHA**: `%s`\n", p.GitTreeSHA))
		if len(p.DoDChecks) > 0 {
			sb.WriteString("- **Definition of Done (DoD) Checks**:\n")
			for _, dod := range p.DoDChecks {
				icon := "✅"
				if dod.Status != "PASSED" {
					icon = "❌"
				}
				sb.WriteString(fmt.Sprintf("  - %s **%s**: %s (`%.2fms`)\n", icon, dod.Description, dod.Evidence, dod.DurationMs))
			}
		}
	}

	// 4. Affected Packages
	if len(affected) > 0 {
		sb.WriteString("\n### 📦 Affected Packages in Monorepo\n")
		for _, pkg := range affected {
			sb.WriteString(fmt.Sprintf("- `%s`\n", pkg))
		}
	}

	// 5. Stacked Diff Tiers
	if stack != nil {
		sb.WriteString("\n### 🥞 Stacked PR Review Slices\n")
		sb.WriteString(fmt.Sprintf("- **Tier 1 (Contracts & Types)**: %d files\n", len(stack.TypesAndContracts)))
		sb.WriteString(fmt.Sprintf("- **Tier 2 (Core Logic)**: %d files\n", len(stack.CoreImplementation)))
		sb.WriteString(fmt.Sprintf("- **Tier 3 (Tests & Consumers)**: %d files\n", len(stack.TestsAndConsumers)))
	}

	sb.WriteString("\n---\n*Authored autonomously and verified by [MonoFlow](https://github.com/M0xoo/monoflow)*\n")

	return sb.String()
}

// ensureBranchAtHEAD creates branch at HEAD without checking it out. An existing branch
// is only accepted if it already points at HEAD; it is never force-reset.
func (m *PRManager) ensureBranchAtHEAD(branch string) error {
	head, err := exec.Command("git", "-C", m.RootDir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("cannot resolve HEAD: %w", err)
	}
	if cur, err := exec.Command("git", "-C", m.RootDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output(); err == nil {
		if strings.TrimSpace(string(cur)) != strings.TrimSpace(string(head)) {
			return fmt.Errorf("branch '%s' already exists at a different commit; commit on it or pass --head=<new-branch>", branch)
		}
		return nil
	}
	if out, err := exec.Command("git", "-C", m.RootDir, "branch", branch, "HEAD").CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create head branch '%s': %v (%s)", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// openRemotePR opens the hosted PR via gh, falling back to the GitHub REST API with
// GH_TOKEN/GITHUB_TOKEN. When neither works it returns a not_created result carrying the
// reason and a compare URL instead of failing silently, so agents in cloud sandboxes can
// hand the already gate-checked PR to their platform's GitHub integration.
func (m *PRManager) openRemotePR(opts CreateOptions, body, base, head string, pushed bool, pushErr string) *RemotePRResult {
	owner, repo, isGitHub := m.getRemoteOriginRepo()
	res := &RemotePRResult{Status: "not_created"}
	if isGitHub {
		res.CompareURL = fmt.Sprintf("https://github.com/%s/%s/compare/%s...%s?expand=1", owner, repo, base, head)
	}
	if !pushed {
		res.Reason = "head branch was not pushed: " + pushErr
		res.Directive = "Fix the push error (or run 'monoflow push'), then re-run 'monoflow pr create'."
		return res
	}
	if !isGitHub {
		res.Status = "skipped"
		res.Reason = "origin is not a GitHub remote; the MonoFlow PR dossier is the review record"
		return res
	}

	var reasons []string
	if _, err := exec.LookPath("gh"); err == nil {
		args := []string{"pr", "create", "--title", opts.Title, "--body", body, "--base", base, "--head", head}
		if opts.Draft {
			args = append(args, "--draft")
		}
		cmd := exec.Command("gh", args...)
		cmd.Dir = m.RootDir
		out, err := cmd.CombinedOutput()
		if err == nil {
			return &RemotePRResult{Status: "created", Via: "gh", CompareURL: strings.TrimSpace(string(out))}
		}
		reasons = append(reasons, "gh: "+firstLine(string(out)))
	} else {
		reasons = append(reasons, "gh: not installed")
	}

	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token != "" {
		url, err := createPRViaAPI(token, owner, repo, opts.Title, body, base, head, opts.Draft)
		if err == nil {
			return &RemotePRResult{Status: "created", Via: "api", CompareURL: url}
		}
		reasons = append(reasons, "api: "+err.Error())
	} else {
		reasons = append(reasons, "api: GH_TOKEN/GITHUB_TOKEN not set")
	}

	res.Reason = strings.Join(reasons, "; ")
	res.Directive = "Branch is pushed and passed the MonoFlow gate. Open the PR with your environment's GitHub integration using this output's title and body (base " + base + ", head " + head + "), or set GH_TOKEN and re-run."
	return res
}

func createPRViaAPI(token, owner, repo, title, body, base, head string, draft bool) (string, error) {
	payload, _ := json.Marshal(map[string]any{"title": title, "body": body, "base": base, "head": head, "draft": draft})
	req, err := http.NewRequest("POST", fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls", owner, repo), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		HTMLURL string `json:"html_url"`
		Message string `json:"message"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("HTTP %d %s", resp.StatusCode, out.Message)
	}
	return out.HTMLURL, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
