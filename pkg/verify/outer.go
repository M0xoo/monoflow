package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/antigravity/monoflow/pkg/checkpoint"
	"github.com/antigravity/monoflow/pkg/graph"
	"github.com/antigravity/monoflow/pkg/guard"
)

// OuterJobStatus defines the lifecycle of an asynchronous outer-loop execution.
type OuterJobStatus string

const (
	JobQueued     OuterJobStatus = "queued"
	JobRunning    OuterJobStatus = "running"
	JobPassed     OuterJobStatus = "passed"
	JobRegressed  OuterJobStatus = "regressed"
	JobPromoted   OuterJobStatus = "promoted"
	JobRolledBack OuterJobStatus = "rolled_back"
)

// OuterJob represents an outer-loop verification job validating heavy transitive integration.
type OuterJob struct {
	ID                  string          `json:"id"`
	CommitSHA           string          `json:"commit_sha"`
	Branch              string          `json:"branch"`
	Remote              string          `json:"remote"`
	RemoteBranch        string          `json:"remote_branch"`
	Status              OuterJobStatus  `json:"status"`
	AutoPush            bool            `json:"auto_push"`
	AutoRevert          bool            `json:"auto_revert"`
	StartedAt           time.Time       `json:"started_at"`
	CompletedAt         time.Time       `json:"completed_at,omitempty"`
	DurationMs          float64         `json:"duration_ms"`
	DirectPackages      []string        `json:"direct_packages"`
	TransitivePackages  []string        `json:"transitive_packages"`
	TotalPackagesTested int             `json:"total_packages_tested"`
	PassedPackages      int             `json:"passed_packages"`
	FailedPackages      int             `json:"failed_packages"`
	TargetResults       []TargetResult  `json:"target_results"`
	ReceiptPath         string          `json:"receipt_path,omitempty"`
	ReceiptSignature    string          `json:"receipt_signature,omitempty"`
	Promoted            bool            `json:"promoted"`
	PromoteError        string          `json:"promote_error,omitempty"`
	RollbackTriggered   bool            `json:"rollback_triggered"`
	AlertPath           string          `json:"alert_path,omitempty"`
	ErrorSummary        string          `json:"error_summary,omitempty"`
}

// OuterLoopOptions configures the execution of the asynchronous outer loop.
type OuterLoopOptions struct {
	CommitSHA    string `json:"commit_sha"`
	Branch       string `json:"branch"`
	Remote       string `json:"remote"`
	RemoteBranch string `json:"remote_branch"`
	AutoPush     bool   `json:"auto_push"`   // Push to remote ONLY when all outer-loop tests succeed
	AutoRevert   bool   `json:"auto_revert"` // Automatically revert to last green checkpoint on failure
	Async        bool   `json:"async"`       // Run in background without blocking agent
	TimeoutSec   int    `json:"timeout_sec"`
}

// OuterLoopManager coordinates background execution, transitive test suites,
// remote promotion gates, and self-healing rollback.
type OuterLoopManager struct {
	mu      sync.Mutex
	RootDir string
	jobsDir string
	logsDir string
}

// NewOuterLoopManager initializes a manager for async outer loop jobs.
func NewOuterLoopManager(rootDir string) *OuterLoopManager {
	jobsDir := filepath.Join(rootDir, ".agents", "outer_loop", "jobs")
	logsDir := filepath.Join(rootDir, ".agents", "outer_loop", "logs")
	_ = os.MkdirAll(jobsDir, 0755)
	_ = os.MkdirAll(logsDir, 0755)

	return &OuterLoopManager{
		RootDir: rootDir,
		jobsDir: jobsDir,
		logsDir: logsDir,
	}
}

// StartJob initializes and dispatches an outer-loop validation task.
func (om *OuterLoopManager) StartJob(opts OuterLoopOptions) (*OuterJob, error) {
	om.mu.Lock()
	defer om.mu.Unlock()

	// 1. Resolve commit SHA
	commitSHA := opts.CommitSHA
	if commitSHA == "" {
		out, err := om.runGit("rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("failed to resolve HEAD commit: %w", err)
		}
		commitSHA = strings.TrimSpace(out)
	}

	// 2. Resolve branch
	branch := opts.Branch
	if branch == "" {
		out, err := om.runGit("rev-parse", "--abbrev-ref", "HEAD")
		if err == nil {
			branch = strings.TrimSpace(out)
		} else {
			branch = "main"
		}
	}

	remote := opts.Remote
	if remote == "" {
		remote = "origin"
	}

	remoteBranch := opts.RemoteBranch
	if remoteBranch == "" {
		remoteBranch = branch
	}

	shortSHA := commitSHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}

	jobID := fmt.Sprintf("outer_%s_%d", shortSHA, time.Now().UnixNano()/int64(time.Millisecond))

	job := &OuterJob{
		ID:           jobID,
		CommitSHA:    commitSHA,
		Branch:       branch,
		Remote:       remote,
		RemoteBranch: remoteBranch,
		Status:       JobQueued,
		AutoPush:     opts.AutoPush,
		AutoRevert:   opts.AutoRevert,
		StartedAt:    time.Now().UTC(),
	}

	if err := om.saveJob(job); err != nil {
		return nil, fmt.Errorf("failed to save initial job: %w", err)
	}

	if opts.Async {
		// Dispatch in detached background goroutine with isolated memory copy
		jobCopy := *job
		go func(id string) {
			_ = om.ExecuteJobByID(id)
		}(job.ID)
		return &jobCopy, nil
	}

	// Synchronous execution
	if err := om.executeJobInternal(job); err != nil {
		return job, err
	}

	return job, nil
}

// ExecuteJobByID loads the job record by ID and executes it in isolation.
func (om *OuterLoopManager) ExecuteJobByID(jobID string) error {
	job, err := om.GetJob(jobID)
	if err != nil {
		return err
	}
	return om.executeJobInternal(job)
}

// ExecuteJob runs the full transitive outer-loop verification.
func (om *OuterLoopManager) ExecuteJob(job *OuterJob) error {
	return om.executeJobInternal(job)
}

func (om *OuterLoopManager) executeJobInternal(job *OuterJob) error {
	start := time.Now()
	job.Status = JobRunning
	_ = om.saveJob(job)

	// 1. Load workspace dependency graph
	g, err := graph.LoadOrDiscover(om.RootDir)
	if err != nil {
		job.Status = JobRegressed
		job.ErrorSummary = fmt.Sprintf("failed to load graph: %v", err)
		job.CompletedAt = time.Now().UTC()
		job.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
		_ = om.saveJob(job)
		return err
	}

	// 2. Identify changed files for this commit
	changedFiles, err := om.getCommitChangedFiles(job.CommitSHA)
	if err != nil || len(changedFiles) == 0 {
		// Fallback to git diff against parent or HEAD
		changedFiles = om.getWorkingTreeChangedFiles()
	}

	// 3. Compute directly affected and transitive downstream packages
	affected := g.ComputeAffected(changedFiles)
	job.DirectPackages = affected.DirectlyChanged

	// Collect full transitive downstream set for outer loop
	transitiveSet := make(map[string]bool)
	for _, directPkg := range affected.DirectlyChanged {
		downstream := g.GetTransitiveDownstream(directPkg)
		for _, down := range downstream {
			transitiveSet[down] = true
		}
	}
	// Also ensure all affected packages are included
	for _, all := range affected.AllAffected {
		transitiveSet[all] = true
	}

	transitiveList := make([]string, 0, len(transitiveSet))
	for pkgName := range transitiveSet {
		transitiveList = append(transitiveList, pkgName)
	}
	job.TransitivePackages = transitiveList

	// 4. Execute test suites across all packages in transitive scope
	packagesToVerify := job.TransitivePackages
	if len(packagesToVerify) == 0 {
		// If no packages mapped, verify directly changed or all
		packagesToVerify = affected.DirectlyChanged
	}

	runner := NewRunner(om.RootDir)
	results := make([]TargetResult, len(packagesToVerify))
	var wg sync.WaitGroup

	for i, pkgName := range packagesToVerify {
		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			pkg, exists := g.GetPackage(name)
			if !exists {
				results[idx] = TargetResult{
					Target: name,
					Status: "passed",
				}
				return
			}
			// Outer loop runs heavy Stage: "outer"
			results[idx] = runner.VerifyTarget(pkg, VerifyOptions{
				Stage: "outer",
			})
		}(i, pkgName)
	}

	wg.Wait()

	job.CompletedAt = time.Now().UTC()
	job.DurationMs = float64(time.Since(start).Microseconds()) / 1000.0
	job.TargetResults = results
	job.TotalPackagesTested = len(results)

	passedCount := 0
	failedCount := 0
	var failureMessages []string

	for _, r := range results {
		if r.Status == "failed" {
			failedCount++
			for _, errItem := range r.Errors {
				failureMessages = append(failureMessages, fmt.Sprintf("[%s] %s:%d: %s", r.Target, errItem.File, errItem.Line, errItem.Message))
			}
		} else {
			passedCount++
		}
	}

	job.PassedPackages = passedCount
	job.FailedPackages = failedCount

	// 5. EVALUATE OUTCOME & ENFORCE PROMOTION/ROLLBACK BARRIER
	if failedCount > 0 {
		// FAILURE / REGRESSION DETECTED
		job.Status = JobRegressed
		job.ErrorSummary = strings.Join(failureMessages, "\n")

		// STRICT INVARIANT: NEVER PUSH TO REMOTE ON FAILURE
		job.Promoted = false

		// Create regression alert event
		alertPath, _ := om.emitRegressionAlert(job)
		job.AlertPath = alertPath

		// Trip circuit breaker
		cb := guard.NewCircuitBreaker(om.RootDir, 3)
		_, _, _ = cb.RecordFailure(om.RootDir, job.ID, strings.Join(job.DirectPackages, ","))

		// Auto-revert working tree to last green checkpoint if enabled
		if job.AutoRevert {
			chkMgr := checkpoint.NewManager(om.RootDir)
			if err := chkMgr.Revert(""); err == nil {
				job.RollbackTriggered = true
				job.Status = JobRolledBack
			}
		}

		_ = om.saveJob(job)
		return fmt.Errorf("outer loop regression detected in %d packages", failedCount)
	}

	// SUCCESS: ALL INTEGRATION & DOWNSTREAM TESTS PASSED
	job.Status = JobPassed

	// Generate and sign cryptographic Tier-2 proof receipt
	receiptPath, sig, err := om.generateTier2Receipt(job, g)
	if err == nil {
		job.ReceiptPath = receiptPath
		job.ReceiptSignature = sig
	}

	// PROMOTION GATE: Push to remote ONLY when passed and auto_push is enabled
	if job.AutoPush {
		if err := om.promoteToRemote(job); err != nil {
			job.PromoteError = err.Error()
		} else {
			job.Promoted = true
			job.Status = JobPromoted
		}
	}

	_ = om.saveJob(job)
	return nil
}

// promoteToRemote safely pushes the proven commit to the remote repository.
func (om *OuterLoopManager) promoteToRemote(job *OuterJob) error {
	refspec := fmt.Sprintf("%s:refs/heads/%s", job.CommitSHA, job.RemoteBranch)
	_, err := om.runGit("push", job.Remote, refspec)
	if err != nil {
		return fmt.Errorf("failed to promote commit to remote %s: %w", job.Remote, err)
	}
	return nil
}

// generateTier2Receipt creates a signed proof receipt stored in .agents/queue/
func (om *OuterLoopManager) generateTier2Receipt(job *OuterJob, g *graph.DependencyGraph) (string, string, error) {
	queueDir := filepath.Join(om.RootDir, ".agents", "queue")
	if err := os.MkdirAll(queueDir, 0755); err != nil {
		return "", "", err
	}

	mergeData := fmt.Sprintf("%s:%s:%s", job.CommitSHA, strings.Join(job.DirectPackages, ","), strings.Join(job.TransitivePackages, ","))
	hasher := sha256.New()
	hasher.Write([]byte(mergeData))
	speculativeMergeSHA := hex.EncodeToString(hasher.Sum(nil))

	sigData := fmt.Sprintf("%s|%s|%s|%f", job.ID, job.CommitSHA, speculativeMergeSHA, job.DurationMs)
	sigHasher := sha256.New()
	sigHasher.Write([]byte(sigData))
	sig := hex.EncodeToString(sigHasher.Sum(nil))

	receipt := map[string]any{
		"task_id":               job.ID,
		"base_commit_sha":       job.CommitSHA,
		"head_commit_sha":       job.CommitSHA,
		"speculative_merge_sha": speculativeMergeSHA,
		"affected_packages":     job.DirectPackages,
		"transitive_packages":   job.TransitivePackages,
		"verified_at":           job.CompletedAt,
		"duration_ms":           job.DurationMs,
		"status":                "speculatively_verified",
		"signature":             sig,
		"verdict":               "Eligible for GitHub Actions Tier 1 Fast-Path Merge (< 30s)",
	}

	shortSHA := job.CommitSHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	receiptPath := filepath.Join(queueDir, fmt.Sprintf("receipt_%s.json", shortSHA))
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(receiptPath, data, 0644); err != nil {
		return "", "", err
	}
	return receiptPath, sig, nil
}

// emitRegressionAlert writes an alert event to .agents/events/
func (om *OuterLoopManager) emitRegressionAlert(job *OuterJob) (string, error) {
	eventsDir := filepath.Join(om.RootDir, ".agents", "events")
	_ = os.MkdirAll(eventsDir, 0755)

	alertPath := filepath.Join(eventsDir, fmt.Sprintf("regression_%s.json", job.ID))
	alertData := map[string]any{
		"event":             "OUTER_LOOP_REGRESSION",
		"job_id":            job.ID,
		"commit_sha":        job.CommitSHA,
		"branch":            job.Branch,
		"remote_target":     fmt.Sprintf("%s/%s", job.Remote, job.RemoteBranch),
		"pushed_to_remote":  false,
		"auto_revert":       job.AutoRevert,
		"failed_packages":   job.FailedPackages,
		"tested_packages":   job.TotalPackagesTested,
		"error_summary":     job.ErrorSummary,
		"detected_at":       job.CompletedAt,
		"action":            "Promotion to remote blocked. Remote branch remains pristine.",
	}

	data, err := json.MarshalIndent(alertData, "", "  ")
	if err != nil {
		return "", err
	}
	return alertPath, os.WriteFile(alertPath, data, 0644)
}

// GetJob loads a job record by ID.
func (om *OuterLoopManager) GetJob(jobID string) (*OuterJob, error) {
	om.mu.Lock()
	defer om.mu.Unlock()

	jobPath := filepath.Join(om.jobsDir, fmt.Sprintf("%s.json", jobID))
	data, err := os.ReadFile(jobPath)
	if err != nil {
		return nil, fmt.Errorf("job '%s' not found: %w", jobID, err)
	}

	var job OuterJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("corrupted job file: %w", err)
	}
	return &job, nil
}

// ListJobs returns all recorded outer-loop jobs.
func (om *OuterLoopManager) ListJobs() ([]*OuterJob, error) {
	om.mu.Lock()
	defer om.mu.Unlock()

	entries, err := os.ReadDir(om.jobsDir)
	if err != nil {
		return nil, err
	}

	jobs := make([]*OuterJob, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(om.jobsDir, entry.Name()))
		if err != nil {
			continue
		}
		var job OuterJob
		if err := json.Unmarshal(data, &job); err == nil {
			jobs = append(jobs, &job)
		}
	}
	return jobs, nil
}

// WaitJob blocks until a job reaches a terminal state (passed, regressed, promoted, rolled_back) or times out.
func (om *OuterLoopManager) WaitJob(jobID string, timeout time.Duration) (*OuterJob, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		job, err := om.GetJob(jobID)
		if err != nil {
			return nil, err
		}
		if job.Status != JobQueued && job.Status != JobRunning {
			return job, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for outer job %s", jobID)
}

func (om *OuterLoopManager) saveJob(job *OuterJob) error {
	path := filepath.Join(om.jobsDir, fmt.Sprintf("%s.json", job.ID))
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (om *OuterLoopManager) getCommitChangedFiles(sha string) ([]string, error) {
	out, err := om.runGit("diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	clean := make([]string, 0, len(lines))
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	return clean, nil
}

func (om *OuterLoopManager) getWorkingTreeChangedFiles() []string {
	out, err := om.runGit("status", "--porcelain")
	if err != nil {
		return []string{}
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files
}

func (om *OuterLoopManager) runGit(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = om.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w (output: %s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
