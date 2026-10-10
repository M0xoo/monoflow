package cihealth

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu       sync.Mutex
	runs     []Run            // newest first
	jobs     map[string][]Job // "runID-attempt"
	logs     map[int64]string
	jobCalls []string
}

func (f *fakeSource) ListRuns(branch, workflow string, limit int) ([]Run, error) {
	if len(f.runs) > limit {
		return f.runs[:limit], nil
	}
	return f.runs, nil
}

func (f *fakeSource) ListJobs(runID int64, attempt int) ([]Job, error) {
	key := fmt.Sprintf("%d-%d", runID, attempt)
	f.mu.Lock()
	f.jobCalls = append(f.jobCalls, key)
	f.mu.Unlock()
	return f.jobs[key], nil
}

func (f *fakeSource) JobLog(jobID int64) (string, error) { return f.logs[jobID], nil }

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func mkRun(id int64, sha string, hour int, conclusion string, attempt int) Run {
	r := Run{ID: id, Name: "CI", HeadSHA: sha, HeadBranch: "main", Status: "completed", Conclusion: conclusion, RunAttempt: attempt,
		HTMLURL: fmt.Sprintf("https://github.com/o/r/actions/runs/%d", id)}
	r.CreatedAt = t0.Add(time.Duration(hour) * time.Hour)
	r.RunStartedAt = r.CreatedAt
	r.UpdatedAt = r.CreatedAt.Add(5 * time.Minute)
	r.HeadCommit.Message = "commit " + sha + "\n\nbody"
	r.HeadCommit.Author.Name = "dev"
	return r
}

func mkJob(id, runID int64, name, conclusion string, at time.Time, failingStep string) Job {
	j := Job{ID: id, RunID: runID, Name: name, Status: "completed", Conclusion: conclusion, CompletedAt: at,
		HTMLURL: fmt.Sprintf("https://github.com/o/r/actions/runs/%d/job/%d", runID, id)}
	j.Steps = []Step{{Name: "Checkout", Conclusion: "success"}}
	if failingStep != "" {
		j.Steps = append(j.Steps, Step{Name: failingStep, Conclusion: "failure"})
	}
	return j
}

// History (oldest→newest): c1 ✓, c2 ✓, c3 test✗, c4 test✗, c5 ✓ (fix), c6 lint flaky (rerun), c7 test✗, c8 test✗, c9 running.
func scenario() *fakeSource {
	f := &fakeSource{jobs: map[string][]Job{}, logs: map[int64]string{}}
	runs := []Run{
		mkRun(1, "c1", 0, "success", 1),
		mkRun(2, "c2", 1, "success", 1),
		mkRun(3, "c3", 2, "failure", 1),
		mkRun(4, "c4", 3, "failure", 1),
		mkRun(5, "c5", 4, "success", 1),
		mkRun(6, "c6", 5, "success", 2),
		mkRun(7, "c7", 6, "failure", 1),
		mkRun(8, "c8", 7, "failure", 1),
	}
	inflight := mkRun(9, "c9", 8, "", 1)
	inflight.Status = "in_progress"
	runs = append(runs, inflight)
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i]
	}
	f.runs = runs

	at := func(h int) time.Time { return t0.Add(time.Duration(h)*time.Hour + 4*time.Minute) }
	for _, rid := range []int64{3, 4, 7, 8} {
		h := int(rid - 1)
		f.jobs[fmt.Sprintf("%d-1", rid)] = []Job{
			mkJob(rid*10, rid, "test", "failure", at(h), "Run Tests"),
			mkJob(rid*10+1, rid, "lint", "success", at(h), ""),
		}
	}
	f.jobs["6-1"] = []Job{
		mkJob(60, 6, "test", "success", at(5), ""),
		mkJob(61, 6, "lint", "failure", at(5), "golangci-lint"),
	}
	f.jobs["6-2"] = []Job{
		mkJob(62, 6, "test", "success", at(5).Add(10*time.Minute), ""),
		mkJob(63, 6, "lint", "success", at(5).Add(10*time.Minute), ""),
	}
	f.logs[80] = "2026-10-01T16:04:00.0000000Z ##[group]Run go test\n" +
		"2026-10-01T16:04:01.0000000Z === RUN TestFoo\n" +
		"2026-10-01T16:04:02.0000000Z     foo_test.go:12: expected 2, got 3\n" +
		"2026-10-01T16:04:02.0000000Z --- FAIL: TestFoo (0.00s)\n" +
		"2026-10-01T16:04:03.0000000Z ##[error]Process completed with exit code 1.\n"
	return f
}

func analyzeScenario(t *testing.T, filter string) (*Report, *fakeSource) {
	t.Helper()
	src := scenario()
	data, warns, err := Collect(src, CollectOptions{Branch: "main", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) > 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	rep := Analyze(data, Options{Branch: "main", CheckFilter: filter, Now: t0.Add(9 * time.Hour)})
	return rep, src
}

func findCheck(rep *Report, name string) *CheckHealth {
	for i := range rep.Checks {
		if rep.Checks[i].Check == name {
			return &rep.Checks[i]
		}
	}
	return nil
}

func TestCollectFetchesOnlyInformativeJobs(t *testing.T) {
	_, src := analyzeScenario(t, "")
	got := map[string]bool{}
	for _, k := range src.jobCalls {
		got[k] = true
	}
	// failed runs, newest green run of the workflow (run 6, latest attempt), its earlier attempt.
	want := []string{"3-1", "4-1", "7-1", "8-1", "6-2", "6-1"}
	for _, k := range want {
		if !got[k] {
			t.Errorf("expected jobs fetch for %s, calls=%v", k, src.jobCalls)
		}
	}
	if len(src.jobCalls) != len(want) {
		t.Errorf("expected %d job fetches, got %d: %v", len(want), len(src.jobCalls), src.jobCalls)
	}
}

func TestIncidentHistoryAndRecurrence(t *testing.T) {
	rep, _ := analyzeScenario(t, "")
	test := findCheck(rep, "CI / test")
	if test == nil {
		t.Fatal("missing check CI / test")
	}
	if test.State != "failing" {
		t.Errorf("state = %s, want failing", test.State)
	}
	if test.Timeline != "PPFFPPFF" {
		t.Errorf("timeline = %s, want PPFFPPFF", test.Timeline)
	}
	if test.IncidentCount != 2 {
		t.Fatalf("incidents = %d, want 2", test.IncidentCount)
	}
	first := test.Incidents[0]
	if first.Open || first.FirstFailingCommit.SHA != "c3" || first.LastPassingCommit.SHA != "c2" || first.FixedByCommit.SHA != "c5" {
		t.Errorf("first incident wrong: %+v", first)
	}
	if first.FailingCommits != 2 || first.FirstFailingCommit.Message != "commit c3" {
		t.Errorf("first incident details wrong: %+v", first)
	}
	if test.MTTRMinutes != 121 { // c3 job failed 11:04, fixing run c5 finished 13:05
		t.Errorf("mttr = %v, want 121", test.MTTRMinutes)
	}
	rec := test.Recurrence
	if rec == nil || !rec.SameFailingStep || rec.PreviousFixedBy == nil || rec.PreviousFixedBy.SHA != "c5" || rec.PreviousBrokenBy.SHA != "c3" {
		t.Fatalf("recurrence wrong: %+v", rec)
	}
	if !strings.Contains(rec.Hint, "git show c5") {
		t.Errorf("hint should point at previous fix: %s", rec.Hint)
	}
}

func TestFailingNowDigest(t *testing.T) {
	rep, _ := analyzeScenario(t, "")
	if len(rep.FailingNow) != 1 {
		t.Fatalf("failing_now = %d, want 1", len(rep.FailingNow))
	}
	fn := rep.FailingNow[0]
	if fn.Check != "CI / test" || fn.FirstFailingCommit.SHA != "c7" || fn.LastPassingCommit.SHA != "c6" {
		t.Errorf("digest wrong: %+v", fn)
	}
	if len(rep.BrokenBy) != 1 || rep.BrokenBy[0].Commit.SHA != "c7" || len(rep.BrokenBy[0].Checks) != 1 {
		t.Errorf("breaking commits = %+v", rep.BrokenBy)
	}
	if fn.FailingCommits != 2 || len(fn.FailingSteps) != 1 || fn.FailingSteps[0] != "Run Tests" {
		t.Errorf("digest details wrong: %+v", fn)
	}
	if fn.FailingForMinutes != 176 { // c7 job completed at 15:04, now 18:00
		t.Errorf("failing for = %v", fn.FailingForMinutes)
	}
	joined := strings.Join(fn.NextSteps, "\n")
	if !strings.Contains(joined, "git log --oneline c6..c7") || !strings.Contains(joined, "git show c5") {
		t.Errorf("next steps missing bisect/previous-fix hints: %v", fn.NextSteps)
	}
}

func TestFlakyDetectionFromRerun(t *testing.T) {
	rep, _ := analyzeScenario(t, "")
	lint := findCheck(rep, "CI / lint")
	if lint == nil {
		t.Fatal("missing lint check")
	}
	if lint.State != "flaky" || lint.FlakyCommits != 1 {
		t.Errorf("lint state=%s flaky=%d, want flaky/1", lint.State, lint.FlakyCommits)
	}
	if lint.Timeline != "PPPPP~PP" {
		t.Errorf("lint timeline = %s", lint.Timeline)
	}
	if lint.IncidentCount != 0 {
		t.Errorf("rerun-passed flake must not open an incident: %+v", lint.Incidents)
	}
}

func TestSummaryMetrics(t *testing.T) {
	rep, _ := analyzeScenario(t, "")
	s := rep.Summary
	if s.Health != "broken" || s.BranchStatus != "red" {
		t.Errorf("health=%s branch=%s", s.Health, s.BranchStatus)
	}
	if s.RunsAnalyzed != 8 || s.InFlightRuns != 1 || s.CommitsAnalyzed != 9 {
		t.Errorf("counts wrong: %+v", s)
	}
	if s.RunSuccessRate != 0.5 {
		t.Errorf("run success rate = %v", s.RunSuccessRate)
	}
	if s.LastGreenCommit == nil || s.LastGreenCommit.SHA != "c6" {
		t.Errorf("last green = %+v", s.LastGreenCommit)
	}
	if s.FailingChecks != 1 || s.FlakyChecks != 1 || s.RecurringFailures != 1 || s.IncidentsInWindow != 2 {
		t.Errorf("check metrics wrong: %+v", s)
	}
	if len(rep.InFlight) != 1 || rep.InFlight[0].SHA != "c9" {
		t.Errorf("in flight = %+v", rep.InFlight)
	}
	if rep.Checks[0].Check != "CI / test" {
		t.Errorf("failing checks must sort first, got %s", rep.Checks[0].Check)
	}
	if len(rep.Workflows) != 1 || rep.Workflows[0].MedianDurationSec != 300 {
		t.Errorf("workflows = %+v", rep.Workflows)
	}
}

func TestCheckFilter(t *testing.T) {
	rep, _ := analyzeScenario(t, "LINT")
	if len(rep.Checks) != 1 || rep.Checks[0].Check != "CI / lint" {
		t.Errorf("filter kept %+v", rep.Checks)
	}
	if len(rep.FailingNow) != 0 {
		t.Errorf("filter should drop unrelated failing checks")
	}
	if rep.Summary.FailingChecks != 1 {
		t.Errorf("summary must stay global under a filter")
	}
}

func TestFailureBeforeWindow(t *testing.T) {
	src := &fakeSource{jobs: map[string][]Job{}}
	src.runs = []Run{mkRun(2, "b", 1, "failure", 1), mkRun(1, "a", 0, "failure", 1)}
	src.jobs["1-1"] = []Job{mkJob(10, 1, "test", "failure", t0, "Run Tests")}
	src.jobs["2-1"] = []Job{mkJob(20, 2, "test", "failure", t0.Add(time.Hour), "Run Tests")}
	data, _, _ := Collect(src, CollectOptions{Limit: 10})
	rep := Analyze(data, Options{Branch: "main", Now: t0.Add(2 * time.Hour)})
	if len(rep.FailingNow) != 1 || !rep.FailingNow[0].StartedBeforeWindow || rep.FailingNow[0].LastPassingCommit != nil {
		t.Fatalf("expected pre-window failure: %+v", rep.FailingNow)
	}
	if !strings.Contains(strings.Join(rep.FailingNow[0].NextSteps, "\n"), "--limit") {
		t.Errorf("should suggest widening the window: %v", rep.FailingNow[0].NextSteps)
	}
}

func TestEmptyHistory(t *testing.T) {
	rep := Analyze(nil, Options{Branch: "main"})
	if rep.Summary.Health != "unknown" || rep.Summary.BranchStatus != "unknown" || rep.FailingNow == nil {
		t.Errorf("empty report wrong: %+v", rep.Summary)
	}
}

func TestExtractErrors(t *testing.T) {
	lines := ExtractErrors(scenario().logs[80], 10)
	if len(lines) != 3 || lines[0] != "    foo_test.go:12: expected 2, got 3" || lines[1] != "--- FAIL: TestFoo (0.00s)" || !strings.HasPrefix(lines[2], "##[error]") {
		t.Errorf("unexpected excerpt: %q", lines)
	}
}

func TestEnrichWithLocalGit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@b", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@b")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(file, msg string) string {
		if err := os.WriteFile(dir+"/"+file, []byte(msg), 0644); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("commit", "-q", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q")
	good := commit("a.go", "good")
	_ = commit("b.go", "refactor b")
	bad := commit("c.go", "break c")

	rep := &Report{FailingNow: []FailingNow{{
		Check:              "CI / test",
		LastPassingCommit:  &CommitRef{SHA: good, Short: short(good)},
		FirstFailingCommit: CommitRef{SHA: bad, Short: short(bad)},
		latestFailingJobID: 80,
	}}}
	Enrich(rep, scenario(), EnrichOptions{RootDir: dir, Logs: true})
	fn := rep.FailingNow[0]
	if len(fn.SuspectCommits) != 2 || fn.SuspectCommits[0].Message != "break c" || fn.SuspectCommits[1].Message != "refactor b" {
		t.Errorf("suspects = %+v", fn.SuspectCommits)
	}
	if strings.Join(fn.SuspectFiles, ",") != "b.go,c.go" {
		t.Errorf("suspect files = %v", fn.SuspectFiles)
	}
	if fn.SuspectCommits[0].Time.IsZero() {
		t.Errorf("suspect commit time not parsed")
	}
	if len(fn.ErrorExcerpt) != 3 {
		t.Errorf("excerpt = %v", fn.ErrorExcerpt)
	}
}

func TestFailFastCancelledJobWithFailedStepCountsAsFailure(t *testing.T) {
	j := mkJob(1, 1, "test (1.22)", "cancelled", t0, "Run Tests")
	if jobOutcome(&j) != OutcomeFail {
		t.Errorf("cancelled job with failed step must count as failure")
	}
	c := mkJob(2, 1, "test (1.23)", "cancelled", t0, "")
	if jobOutcome(&c) != OutcomeCancelled {
		t.Errorf("plain cancelled job must stay cancelled")
	}
}
