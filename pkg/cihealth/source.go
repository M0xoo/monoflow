package cihealth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Run is a GitHub Actions workflow run (subset of the REST payload).
type Run struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	WorkflowID   int64     `json:"workflow_id"`
	HeadSHA      string    `json:"head_sha"`
	HeadBranch   string    `json:"head_branch"`
	Event        string    `json:"event"`
	Status       string    `json:"status"`
	Conclusion   string    `json:"conclusion"`
	RunAttempt   int       `json:"run_attempt"`
	HTMLURL      string    `json:"html_url"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	HeadCommit   struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"head_commit"`
}

// Step is a single step of a job.
type Step struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Number     int    `json:"number"`
}

// Job is a GitHub Actions job — the unit that shows up as a "check" on a commit.
type Job struct {
	ID          int64     `json:"id"`
	RunID       int64     `json:"run_id"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	HTMLURL     string    `json:"html_url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Steps       []Step    `json:"steps"`
}

// Source abstracts the CI provider so analysis can be tested offline.
type Source interface {
	ListRuns(branch, workflow string, limit int) ([]Run, error)
	ListJobs(runID int64, attempt int) ([]Job, error)
	JobLog(jobID int64) (string, error)
}

// GHSource reads GitHub Actions history through the authenticated `gh` CLI.
// Jobs of completed runs are immutable and cached under .agents/cache/ci-health.
type GHSource struct {
	RootDir  string
	Timeout  time.Duration
	cacheDir string
}

// NewGHSource creates a gh-backed source rooted at the repository.
func NewGHSource(rootDir string) *GHSource {
	return &GHSource{
		RootDir:  rootDir,
		Timeout:  20 * time.Second,
		cacheDir: filepath.Join(rootDir, ".agents", "cache", "ci-health", "jobs"),
	}
}

func (s *GHSource) api(path string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh CLI not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "-H", "Accept: application/vnd.github+json", path)
	cmd.Dir = s.RootDir
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("gh api %s: %s", path, msg)
	}
	return out, nil
}

// ListRuns returns the most recent workflow runs on a branch, newest first.
func (s *GHSource) ListRuns(branch, workflow string, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 50
	}
	base := "repos/{owner}/{repo}/actions/runs"
	if workflow != "" {
		base = fmt.Sprintf("repos/{owner}/{repo}/actions/workflows/%s/runs", url.PathEscape(workflow))
	}
	var runs []Run
	for page := 1; len(runs) < limit; page++ {
		q := url.Values{}
		q.Set("per_page", "100")
		q.Set("page", fmt.Sprint(page))
		if branch != "" {
			q.Set("branch", branch)
		}
		out, err := s.api(base + "?" + q.Encode())
		if err != nil {
			return nil, err
		}
		var payload struct {
			WorkflowRuns []Run `json:"workflow_runs"`
		}
		if err := json.Unmarshal(out, &payload); err != nil {
			return nil, fmt.Errorf("decode workflow runs: %w", err)
		}
		runs = append(runs, payload.WorkflowRuns...)
		if len(payload.WorkflowRuns) < 100 {
			break
		}
	}
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

// ListJobs returns the jobs of the given run attempt.
func (s *GHSource) ListJobs(runID int64, attempt int) ([]Job, error) {
	if attempt < 1 {
		attempt = 1
	}
	cachePath := filepath.Join(s.cacheDir, fmt.Sprintf("%d-%d.json", runID, attempt))
	if data, err := os.ReadFile(cachePath); err == nil {
		var jobs []Job
		if json.Unmarshal(data, &jobs) == nil {
			return jobs, nil
		}
	}
	out, err := s.api(fmt.Sprintf("repos/{owner}/{repo}/actions/runs/%d/attempts/%d/jobs?per_page=100", runID, attempt))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Jobs []Job `json:"jobs"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}
	completed := true
	for _, j := range payload.Jobs {
		if j.Status != "completed" {
			completed = false
		}
	}
	if completed && len(payload.Jobs) > 0 {
		if err := os.MkdirAll(s.cacheDir, 0755); err == nil {
			if data, err := json.Marshal(payload.Jobs); err == nil {
				_ = os.WriteFile(cachePath, data, 0644)
			}
		}
	}
	return payload.Jobs, nil
}

// JobLog returns the raw log of a job.
func (s *GHSource) JobLog(jobID int64) (string, error) {
	out, err := s.api(fmt.Sprintf("repos/{owner}/{repo}/actions/jobs/%d/logs", jobID))
	if err != nil {
		return "", err
	}
	return string(out), nil
}
