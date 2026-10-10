package cihealth

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// CollectOptions controls how much history is pulled from the provider.
type CollectOptions struct {
	Branch      string
	Workflow    string
	Limit       int
	Concurrency int
}

// Collect fetches run history and the job details needed for check-level analysis.
//
// To keep API usage low, jobs are fetched only where they carry information:
// failed runs (which job/step broke), the newest green run of each workflow
// (to learn its job names, so other green runs can be inferred as all-pass),
// and every earlier attempt of re-run workflows (flakiness evidence).
func Collect(src Source, opts CollectOptions) ([]RunData, []string, error) {
	runs, err := src.ListRuns(opts.Branch, opts.Workflow, opts.Limit)
	if err != nil {
		return nil, nil, err
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}

	data := make([]RunData, len(runs))
	type req struct{ run, attempt int }
	var reqs []req
	greenSeen := map[string]bool{}
	for i, r := range runs { // newest first
		attempts := r.RunAttempt
		if attempts < 1 {
			attempts = 1
		}
		data[i] = RunData{Run: r, Attempts: make([][]Job, attempts)}
		if r.Status != "completed" {
			continue
		}
		out := outcomeOf(r.Conclusion)
		latest := out == OutcomeFail
		if out == OutcomePass && !greenSeen[r.Name] {
			greenSeen[r.Name] = true
			latest = true
		}
		if latest || attempts > 1 {
			reqs = append(reqs, req{i, attempts})
		}
		for a := 1; a < attempts; a++ {
			reqs = append(reqs, req{i, a})
		}
	}

	var mu sync.Mutex
	var warnings []string
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.Concurrency)
	for _, rq := range reqs {
		wg.Add(1)
		sem <- struct{}{}
		go func(rq req) {
			defer wg.Done()
			defer func() { <-sem }()
			jobs, err := src.ListJobs(data[rq.run].Run.ID, rq.attempt)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("jobs for run %d attempt %d: %v", data[rq.run].Run.ID, rq.attempt, err))
				return
			}
			if jobs == nil {
				jobs = []Job{}
			}
			data[rq.run].Attempts[rq.attempt-1] = jobs
		}(rq)
	}
	wg.Wait()
	return data, warnings, nil
}

// EnrichOptions controls the post-analysis enrichment.
type EnrichOptions struct {
	RootDir  string
	Logs     bool
	MaxFiles int
}

// Enrich adds local-git suspect ranges and, optionally, log excerpts to failing checks.
func Enrich(rep *Report, src Source, opts EnrichOptions) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 40
	}
	for i := range rep.FailingNow {
		fn := &rep.FailingNow[i]
		if fn.LastPassingCommit != nil {
			from, to := fn.LastPassingCommit.SHA, fn.FirstFailingCommit.SHA
			if commits, err := gitSuspects(opts.RootDir, from, to); err == nil {
				fn.SuspectCommits = commits
			} else {
				fn.SuspectCommits = []CommitRef{fn.FirstFailingCommit}
				rep.Warnings = appendOnce(rep.Warnings, "suspect range unavailable locally (commits not fetched); run `monoflow sync` for full suspect lists")
			}
			if files, err := gitLines(opts.RootDir, "diff", "--name-only", from, to); err == nil {
				if len(files) > opts.MaxFiles {
					files = append(files[:opts.MaxFiles], fmt.Sprintf("... (%d more)", len(files)-opts.MaxFiles))
				}
				fn.SuspectFiles = files
			}
		}
		if opts.Logs && fn.latestFailingJobID > 0 && src != nil {
			if raw, err := src.JobLog(fn.latestFailingJobID); err == nil {
				fn.ErrorExcerpt = ExtractErrors(raw, 25)
			} else {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("log for %s: %v", fn.Check, err))
			}
		}
		for _, ch := range rep.Checks {
			if ch.Check == fn.Check {
				fn.NextSteps = nextSteps(&ch, fn)
				break
			}
		}
	}
}

func gitSuspects(root, from, to string) ([]CommitRef, error) {
	lines, err := gitLines(root, "log", "--format=%H%x1f%s%x1f%an%x1f%cI", from+".."+to)
	if err != nil {
		return nil, err
	}
	var out []CommitRef
	for _, l := range lines {
		p := strings.Split(l, "\x1f")
		if len(p) < 3 {
			continue
		}
		c := CommitRef{SHA: p[0], Short: short(p[0]), Message: p[1], Author: p[2]}
		if len(p) > 3 {
			c.Time, _ = time.Parse(time.RFC3339, p[3])
		}
		out = append(out, c)
	}
	return out, nil
}

func gitLines(root string, args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

var (
	logTimestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z\s?`)
	ansiEscape   = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	errorLine    = regexp.MustCompile(`(?i)(##\[error\]|\berror\b|\bfail(ed|ure)?\b|panic:|fatal|exception|assert|traceback|undefined|cannot|exit code [1-9]|\S+\.\w+:\d+)`)
)

// ExtractErrors returns the most relevant error lines of a CI log, newest last.
func ExtractErrors(raw string, max int) []string {
	var cleaned, hits []string
	for _, l := range strings.Split(raw, "\n") {
		l = ansiEscape.ReplaceAllString(logTimestamp.ReplaceAllString(strings.TrimRight(l, "\r"), ""), "")
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "##[group]") || strings.HasPrefix(l, "##[endgroup]") {
			continue
		}
		if len(l) > 300 {
			l = l[:300] + "…"
		}
		cleaned = append(cleaned, l)
		if errorLine.MatchString(l) && !strings.Contains(l, "--- PASS") {
			hits = append(hits, l)
		}
	}
	src := hits
	if len(src) == 0 {
		src = cleaned
	}
	if len(src) > max {
		src = src[len(src)-max:]
	}
	return src
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
