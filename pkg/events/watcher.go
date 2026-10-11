package events

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WatchJobStatus tracks the state of the autonomous CI supervisor.
type WatchJobStatus string

const (
	WatchStatusMonitoring      WatchJobStatus = "monitoring"
	WatchStatusCompletedPassed WatchJobStatus = "completed_passed"
	WatchStatusCompletedFailed WatchJobStatus = "completed_failed"
	WatchStatusTimedOut        WatchJobStatus = "timed_out"
)

// WatchJob represents a detached background CI monitoring task.
type WatchJob struct {
	ID           string         `json:"id"`
	CommitSHA    string         `json:"commit_sha"`
	Branch       string         `json:"branch"`
	Status       WatchJobStatus `json:"status"`
	StartedAt    time.Time      `json:"started_at"`
	CompletedAt  time.Time      `json:"completed_at,omitempty"`
	DurationSec  float64        `json:"duration_sec"`
	RunID        int64          `json:"run_id,omitempty"`
	WorkflowName string         `json:"workflow_name,omitempty"`
	WorkflowURL  string         `json:"workflow_url,omitempty"`
	EventEmitted bool           `json:"event_emitted"`
	EventID      string         `json:"event_id,omitempty"`
	FailureLogs  string         `json:"failure_logs,omitempty"`
}

// WatchOptions configures background monitoring of remote CI runs.
type WatchOptions struct {
	CommitSHA       string `json:"commit_sha"`
	Branch          string `json:"branch"`
	PollIntervalSec int    `json:"poll_interval_sec"`
	TimeoutMin      int    `json:"timeout_min"`
	Async           bool   `json:"async"` // Detach in background; return immediately to agent
	Remote          string `json:"remote,omitempty"`
}

// CIWatcher supervises remote CI without burning agent context tokens.
type CIWatcher struct {
	mu       sync.Mutex
	RootDir  string
	watchDir string
	events   *EventManager
	// Spawn starts the detached supervisor process (overridable in tests).
	Spawn func(job *WatchJob, opts WatchOptions) error
}

// NewCIWatcher initializes a background CI supervisor.
func NewCIWatcher(rootDir string) *CIWatcher {
	watchDir := filepath.Join(rootDir, ".agents", "events", "watchers")
	_ = os.MkdirAll(watchDir, 0755)

	return &CIWatcher{
		RootDir:  rootDir,
		watchDir: watchDir,
		events:   NewEventManager(rootDir),
	}
}

// StartWatch initiates CI monitoring. If opts.Async is true, returns immediately in < 50ms.
// A commit that already has an active watcher reuses it instead of starting another.
func (cw *CIWatcher) StartWatch(opts WatchOptions) (*WatchJob, error) {
	if opts.Async && opts.CommitSHA != "" {
		if jobs, err := cw.ListJobs(); err == nil {
			for _, j := range jobs {
				if j.CommitSHA == opts.CommitSHA && j.Status == WatchStatusMonitoring {
					return j, nil
				}
			}
		}
	}
	if opts.CommitSHA != "" {
		NewEventManager(cw.RootDir).AckSkipped(opts.CommitSHA)
	}
	cw.mu.Lock()

	commitSHA := opts.CommitSHA
	if commitSHA == "" {
		cmd := exec.Command("git", "rev-parse", "HEAD")
		cmd.Dir = cw.RootDir
		if out, err := cmd.CombinedOutput(); err == nil {
			commitSHA = strings.TrimSpace(string(out))
		}
	}

	branch := opts.Branch
	if branch == "" {
		cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
		cmd.Dir = cw.RootDir
		if out, err := cmd.CombinedOutput(); err == nil {
			branch = strings.TrimSpace(string(out))
		} else {
			branch = "main"
		}
	}

	shortSHA := commitSHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}

	jobID := fmt.Sprintf("watch_%s_%d", shortSHA, time.Now().UnixNano()/int64(time.Millisecond))

	job := &WatchJob{
		ID:        jobID,
		CommitSHA: commitSHA,
		Branch:    branch,
		Status:    WatchStatusMonitoring,
		StartedAt: time.Now().UTC(),
	}

	if err := cw.saveJob(job); err != nil {
		cw.mu.Unlock()
		return nil, fmt.Errorf("failed to save watch job: %w", err)
	}
	cw.mu.Unlock()

	if opts.Async {
		// The CLI process exits right after replying, which would kill a goroutine, so
		// supervise in a detached child process that outlives it.
		spawn := cw.Spawn
		if spawn == nil {
			spawn = cw.spawnDetached
		}
		if err := spawn(job, opts); err != nil {
			cw.finishJob(job, WatchStatusTimedOut, "failed to start background watcher: "+err.Error())
			return job, err
		}
		return job, nil
	}

	// Synchronous execution (useful for tests or explicit waiting)
	cw.supervise(job, opts)
	return job, nil
}

type ghRunItem struct {
	DatabaseID   int64  `json:"databaseId"`
	Status       string `json:"status"`     // "queued", "in_progress", "completed"
	Conclusion   string `json:"conclusion"` // "success", "failure", "cancelled", etc.
	Name         string `json:"name"`
	URL          string `json:"url"`
	WorkflowName string `json:"workflowName"`
}

// spawnDetached re-executes this binary as `monoflow events watch --job=<id>` in a new
// session so it survives the parent CLI process.
func (cw *CIWatcher) spawnDetached(job *WatchJob, opts WatchOptions) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Only the monoflow CLI can serve as the watcher process (never a test binary, which
	// would re-run its own test suite).
	if !strings.HasPrefix(filepath.Base(exe), "monoflow") {
		if alt, lookErr := exec.LookPath("monoflow"); lookErr == nil {
			exe = alt
		} else {
			return fmt.Errorf("monoflow binary not found for background watcher (running as %s)", filepath.Base(exe))
		}
	}
	args := []string{"events", "watch", job.CommitSHA, "--branch=" + job.Branch, "--job=" + job.ID}
	if opts.Remote != "" {
		args = append(args, "--remote="+opts.Remote)
	}
	if opts.TimeoutMin > 0 {
		args = append(args, fmt.Sprintf("--timeout=%d", opts.TimeoutMin))
	}
	logFile, err := os.OpenFile(filepath.Join(cw.watchDir, job.ID+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, args...)
	cmd.Dir = cw.RootDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// ResumeJob supervises an existing job record (used by the detached watcher process).
func (cw *CIWatcher) ResumeJob(jobID string, opts WatchOptions) (*WatchJob, error) {
	job, err := cw.GetJob(jobID)
	if err != nil {
		return nil, err
	}
	cw.supervise(job, opts)
	return job, nil
}

// remoteIsGitHub reports whether CI results can come from GitHub Actions.
func (cw *CIWatcher) remoteIsGitHub(remote string) bool {
	if remote == "" {
		remote = "origin"
	}
	out, err := exec.Command("git", "-C", cw.RootDir, "remote", "get-url", remote).Output()
	if err != nil {
		return false
	}
	u := strings.TrimSpace(string(out))
	if strings.Contains(u, "github.com") || (strings.HasPrefix(u, "http") && strings.Contains(u, "/git/")) {
		_, ghErr := exec.LookPath("gh")
		return ghErr == nil
	}
	return false
}

// runLocalCI is the CI backend for remotes without GitHub Actions visibility: it clones
// the pushed commit from the remote and runs the repo's ci_cmd (default: full verify).
func (cw *CIWatcher) runLocalCI(job *WatchJob, remote string) {
	if remote == "" {
		remote = "origin"
	}
	tmp, err := os.MkdirTemp("", "monoflow-ci-*")
	if err != nil {
		cw.finishJob(job, WatchStatusTimedOut, err.Error())
		return
	}
	defer os.RemoveAll(tmp)
	url, _ := exec.Command("git", "-C", cw.RootDir, "remote", "get-url", remote).Output()
	src := strings.TrimSpace(string(url))
	if src == "" {
		// No remote: there is no remote CI to report on.
		evt := &Event{Type: EventCISkipped, CommitSHA: job.CommitSHA, Branch: job.Branch,
			Summary: fmt.Sprintf("No remote %q: nothing to run remote CI against", remote)}
		_ = cw.events.Emit(evt)
		job.EventEmitted, job.EventID = true, evt.ID
		cw.finishJob(job, WatchStatusCompletedPassed, "no remote")
		return
	}
	steps := [][]string{
		{"git", "clone", "-q", "--no-checkout", src, tmp},
		{"git", "-C", tmp, "checkout", "-q", job.CommitSHA},
	}
	for _, s := range steps {
		if out, err := exec.Command(s[0], s[1:]...).CombinedOutput(); err != nil {
			cw.emitCIResult(job, false, "local-ci", "failed to fetch pushed commit: "+strings.TrimSpace(string(out)))
			return
		}
	}
	ciCmd := readCICommand(tmp)
	if ciCmd == "" {
		exe, _ := os.Executable()
		ciCmd = fmt.Sprintf("%q verify --all --no-cache", exe)
	}
	cmd := exec.Command("sh", "-c", ciCmd)
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "MONOFLOW_INTERNAL=1", "MONOFLOW_CACHE_DIR="+filepath.Join(tmp, ".agents", "ci-cache"))
	out, err := cmd.CombinedOutput()
	logs := strings.TrimSpace(string(out))
	if len(logs) > 4000 {
		logs = logs[len(logs)-4000:]
	}
	cw.emitCIResult(job, err == nil, "local-ci: "+ciCmd, logs)
}

// readCICommand returns "ci_cmd" from .agents/monoflow.config.json, if set.
func readCICommand(repoDir string) string {
	data, err := os.ReadFile(filepath.Join(repoDir, ".agents", "monoflow.config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CICmd string `json:"ci_cmd"`
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg.CICmd
}

func (cw *CIWatcher) emitCIResult(job *WatchJob, passed bool, workflow, logs string) {
	short := job.CommitSHA[:min(8, len(job.CommitSHA))]
	evt := &Event{
		Type:      EventCIPassed,
		CommitSHA: job.CommitSHA,
		Branch:    job.Branch,
		Summary:   fmt.Sprintf("CI (%s) passed for %s on %s", workflow, short, job.Branch),
		Payload:   map[string]any{"workflow_name": workflow},
	}
	status := WatchStatusCompletedPassed
	if !passed {
		evt.Type = EventCIFailed
		evt.RequiresAction = true
		evt.Summary = fmt.Sprintf("CI (%s) FAILED for %s on %s", workflow, short, job.Branch)
		evt.SuggestedTask = "Reproduce with 'monoflow verify', fix, then 'monoflow ship -m <msg>'"
		evt.Payload["failure_logs"] = logs
		job.FailureLogs = logs
		status = WatchStatusCompletedFailed
	}
	job.WorkflowName = workflow
	_ = cw.events.Emit(evt)
	job.EventEmitted = true
	job.EventID = evt.ID
	cw.finishJob(job, status, "")
}

// supervise runs the background monitoring loop until CI completes or times out.
func (cw *CIWatcher) supervise(job *WatchJob, opts WatchOptions) {
	if !cw.remoteIsGitHub(opts.Remote) {
		cw.runLocalCI(job, opts.Remote)
		return
	}

	interval := time.Duration(opts.PollIntervalSec) * time.Second
	if interval <= 0 {
		interval = 8 * time.Second
	}

	timeout := time.Duration(opts.TimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 25 * time.Minute
	}

	deadline := time.Now().Add(timeout)
	noRunDeadline := time.Now().Add(3 * time.Minute)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if time.Now().After(deadline) {
			cw.finishJob(job, WatchStatusTimedOut, "CI timed out after deadline")
			return
		}

		// 1. Inspect PR mergeability if branch is associated with a pull request
		if job.Branch != "" {
			if prInfo, _ := cw.fetchPRMergeable(job.Branch); prInfo != nil {
				if prInfo.Mergeable == "CONFLICTING" || prInfo.MergeStateStatus == "DIRTY" {
					evt := &Event{
						Type:           EventPRConflict,
						CommitSHA:      job.CommitSHA,
						Branch:         job.Branch,
						RequiresAction: true,
						Summary:        fmt.Sprintf("PR #%d ('%s') has merge conflicts with base branch (mergeStateStatus: %s)", prInfo.Number, prInfo.Title, prInfo.MergeStateStatus),
						SuggestedTask:  "Rebase branch onto base branch using 'monoflow sync origin main --rebase' to resolve conflicts",
						Payload: map[string]any{
							"pr_number":        prInfo.Number,
							"title":            prInfo.Title,
							"mergeable":        prInfo.Mergeable,
							"mergeStateStatus": prInfo.MergeStateStatus,
						},
					}
					_ = cw.events.Emit(evt)
					job.EventEmitted = true
					job.EventID = evt.ID
					cw.finishJob(job, WatchStatusCompletedFailed, fmt.Sprintf("PR #%d has merge conflicts with base branch", prInfo.Number))
					return
				}
			}
		}

		runs, err := cw.fetchRemoteRuns(job.CommitSHA)
		if err == nil && len(runs) == 0 && time.Now().After(noRunDeadline) {
			// The economical workflow only runs for PR heads/main (not drafts or docs-only
			// changes), so "no run" is a legitimate, final outcome; say so instead of
			// leaving the agent waiting for an event that never comes.
			evt := &Event{
				Type:      EventCISkipped,
				CommitSHA: job.CommitSHA,
				Branch:    job.Branch,
				Summary:   fmt.Sprintf("No remote CI run for %s on %s (no open PR, draft, or docs-only change)", job.CommitSHA[:min(8, len(job.CommitSHA))], job.Branch),
			}
			_ = cw.events.Emit(evt)
			job.EventEmitted = true
			job.EventID = evt.ID
			cw.finishJob(job, WatchStatusCompletedPassed, "no CI run triggered")
			return
		}
		if err == nil && len(runs) > 0 {
			allCompleted := true
			hasFailure := false
			var failedRun *ghRunItem
			var primaryRun *ghRunItem

			for i := range runs {
				r := &runs[i]
				if primaryRun == nil {
					primaryRun = r
				}
				if r.Status != "completed" {
					allCompleted = false
					break
				}
				if r.Conclusion == "failure" || r.Conclusion == "startup_failure" {
					hasFailure = true
					failedRun = r
				}
			}

			if allCompleted {
				if hasFailure && failedRun != nil {
					// Failed! Capture logs if possible
					failureLogs := cw.fetchFailedLogs(failedRun.DatabaseID)
					job.RunID = failedRun.DatabaseID
					job.WorkflowName = failedRun.WorkflowName
					job.WorkflowURL = failedRun.URL
					job.FailureLogs = failureLogs

					// Emit failure event to wake up or alert agent
					evt := &Event{
						Type:           EventCIFailed,
						CommitSHA:      job.CommitSHA,
						Branch:         job.Branch,
						RequiresAction: true,
						Summary:        fmt.Sprintf("Remote CI workflow '%s' failed on commit %s", failedRun.WorkflowName, job.CommitSHA[:min(8, len(job.CommitSHA))]),
						SuggestedTask:  "Inspect remote CI failure diagnostics and repair regressed tests",
						Payload: map[string]any{
							"run_id":        failedRun.DatabaseID,
							"workflow_name": failedRun.WorkflowName,
							"workflow_url":  failedRun.URL,
							"failure_logs":  failureLogs,
						},
					}
					_ = cw.events.Emit(evt)
					job.EventEmitted = true
					job.EventID = evt.ID

					cw.finishJob(job, WatchStatusCompletedFailed, "CI failed")
					return
				}

				// All workflows passed!
				if primaryRun != nil {
					job.RunID = primaryRun.DatabaseID
					job.WorkflowName = primaryRun.WorkflowName
					job.WorkflowURL = primaryRun.URL
				}

				// Emit silent success event (no action required)
				evt := &Event{
					Type:           EventCIPassed,
					CommitSHA:      job.CommitSHA,
					Branch:         job.Branch,
					RequiresAction: false,
					Summary:        fmt.Sprintf("All remote CI workflows passed for commit %s", job.CommitSHA[:min(8, len(job.CommitSHA))]),
					Payload: map[string]any{
						"run_id":        job.RunID,
						"workflow_name": job.WorkflowName,
						"workflow_url":  job.WorkflowURL,
					},
				}
				_ = cw.events.Emit(evt)
				job.EventEmitted = true
				job.EventID = evt.ID

				cw.finishJob(job, WatchStatusCompletedPassed, "")
				return
			}
		}

		select {
		case <-ticker.C:
			// Continue loop
		}
	}
}

type ghPRMergeableInfo struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	Mergeable        string `json:"mergeable"`        // "CONFLICTING", "MERGEABLE", "UNKNOWN"
	MergeStateStatus string `json:"mergeStateStatus"` // "DIRTY", "CLEAN", "BLOCKED", "BEHIND"
}

func (cw *CIWatcher) fetchPRMergeable(branch string) (*ghPRMergeableInfo, error) {
	cmd := exec.Command("gh", "pr", "view", branch, "--json", "number,title,mergeable,mergeStateStatus")
	cmd.Dir = cw.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	var info ghPRMergeableInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (cw *CIWatcher) fetchRemoteRuns(commitSHA string) ([]ghRunItem, error) {
	cmd := exec.Command("gh", "run", "list", "--commit", commitSHA, "--json", "databaseId,status,conclusion,name,url,workflowName")
	cmd.Dir = cw.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	var runs []ghRunItem
	if err := json.Unmarshal(out, &runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func (cw *CIWatcher) fetchFailedLogs(runID int64) string {
	if runID <= 0 {
		return ""
	}
	cmd := exec.Command("gh", "run", "view", fmt.Sprintf("%d", runID), "--log-failed")
	cmd.Dir = cw.RootDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	logStr := string(out)
	if len(logStr) > 4000 {
		// Truncate to tail 4000 chars to avoid giant blobs
		logStr = logStr[len(logStr)-4000:]
	}
	return strings.TrimSpace(logStr)
}

func (cw *CIWatcher) finishJob(job *WatchJob, status WatchJobStatus, failureReason string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	job.Status = status
	job.CompletedAt = time.Now().UTC()
	job.DurationSec = job.CompletedAt.Sub(job.StartedAt).Seconds()
	if failureReason != "" && job.FailureLogs == "" {
		job.FailureLogs = failureReason
	}
	_ = cw.saveJob(job)
}

// GetJob retrieves a watch job status by ID.
func (cw *CIWatcher) GetJob(jobID string) (*WatchJob, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	path := filepath.Join(cw.watchDir, fmt.Sprintf("%s.json", jobID))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("watch job '%s' not found: %w", jobID, err)
	}

	var job WatchJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("corrupted watch job file: %w", err)
	}
	return &job, nil
}

// ListJobs returns all recorded watch jobs.
func (cw *CIWatcher) ListJobs() ([]*WatchJob, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	entries, err := os.ReadDir(cw.watchDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*WatchJob{}, nil
		}
		return nil, err
	}

	var jobs []*WatchJob
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(cw.watchDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var job WatchJob
		if err := json.Unmarshal(data, &job); err == nil {
			jobs = append(jobs, &job)
		}
	}
	return jobs, nil
}

func (cw *CIWatcher) saveJob(job *WatchJob) error {
	path := filepath.Join(cw.watchDir, fmt.Sprintf("%s.json", job.ID))
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// writeAtomic writes via a temp file + rename so concurrent readers (hooks, waiters in
// other processes) never see a partial JSON file.
func writeAtomic(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
