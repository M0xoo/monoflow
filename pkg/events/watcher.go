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
	WatchStatusMonitoring     WatchJobStatus = "monitoring"
	WatchStatusCompletedPassed WatchJobStatus = "completed_passed"
	WatchStatusCompletedFailed WatchJobStatus = "completed_failed"
	WatchStatusTimedOut       WatchJobStatus = "timed_out"
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
}

// CIWatcher supervises remote CI without burning agent context tokens.
type CIWatcher struct {
	mu       sync.Mutex
	RootDir  string
	watchDir string
	events   *EventManager
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
func (cw *CIWatcher) StartWatch(opts WatchOptions) (*WatchJob, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()

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
		return nil, fmt.Errorf("failed to save watch job: %w", err)
	}

	if opts.Async {
		// Detach in background goroutine — zero agent blocking!
		go func(j *WatchJob, o WatchOptions) {
			cw.supervise(j, o)
		}(job, opts)
		return job, nil
	}

	// Synchronous execution (useful for tests or explicit waiting)
	cw.supervise(job, opts)
	return job, nil
}

type ghRunItem struct {
	DatabaseID   int64  `json:"databaseId"`
	Status       string `json:"status"`       // "queued", "in_progress", "completed"
	Conclusion   string `json:"conclusion"`   // "success", "failure", "cancelled", etc.
	Name         string `json:"name"`
	URL          string `json:"url"`
	WorkflowName string `json:"workflowName"`
}

// supervise runs the background monitoring loop until CI completes or times out.
func (cw *CIWatcher) supervise(job *WatchJob, opts WatchOptions) {
	interval := time.Duration(opts.PollIntervalSec) * time.Second
	if interval <= 0 {
		interval = 8 * time.Second
	}

	timeout := time.Duration(opts.TimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 25 * time.Minute
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if time.Now().After(deadline) {
			cw.finishJob(job, WatchStatusTimedOut, "CI timed out after deadline")
			return
		}

		runs, err := cw.fetchRemoteRuns(job.CommitSHA)
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
	return os.WriteFile(path, data, 0644)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
