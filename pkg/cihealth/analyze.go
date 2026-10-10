// Package cihealth turns raw GitHub Actions history into an agent-ready CI
// health report: global metrics, per-check pass/fail timelines, and failure
// incidents that pin down when a check broke, which commit broke it, which
// commit fixed it, and whether it has broken the same way before.
package cihealth

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Outcome of a check on a single run.
type Outcome string

const (
	OutcomePass      Outcome = "pass"
	OutcomeFail      Outcome = "fail"
	OutcomeCancelled Outcome = "cancelled"
	OutcomeSkipped   Outcome = "skipped"
)

func outcomeOf(conclusion string) Outcome {
	switch conclusion {
	case "success":
		return OutcomePass
	case "failure", "timed_out", "startup_failure", "action_required":
		return OutcomeFail
	case "cancelled":
		return OutcomeCancelled
	default:
		return OutcomeSkipped
	}
}

// jobOutcome treats a job cancelled by matrix fail-fast as failed when one of
// its own steps had already failed; otherwise the real breakage is hidden.
func jobOutcome(j *Job) Outcome {
	out := outcomeOf(j.Conclusion)
	if out == OutcomeCancelled && len(failingSteps(j)) > 0 {
		return OutcomeFail
	}
	return out
}

// CommitRef identifies a commit in the analyzed window.
type CommitRef struct {
	SHA     string    `json:"sha"`
	Short   string    `json:"short"`
	Message string    `json:"message,omitempty"`
	Author  string    `json:"author,omitempty"`
	Time    time.Time `json:"time"`
	RunURL  string    `json:"run_url,omitempty"`
}

// Incident is one contiguous failure streak of a check.
type Incident struct {
	Open                bool       `json:"open"`
	StartedBeforeWindow bool       `json:"started_before_window,omitempty"`
	StartedAt           time.Time  `json:"started_at"`
	EndedAt             *time.Time `json:"ended_at,omitempty"`
	DurationMinutes     float64    `json:"duration_minutes"`
	FailingCommits      int        `json:"failing_commits"`
	LastPassingCommit   *CommitRef `json:"last_passing_commit,omitempty"`
	FirstFailingCommit  CommitRef  `json:"first_failing_commit"`
	LatestFailingCommit CommitRef  `json:"latest_failing_commit"`
	FixedByCommit       *CommitRef `json:"fixed_by_commit,omitempty"`
	FailingSteps        []string   `json:"failing_steps,omitempty"`
	FailingJobURL       string     `json:"failing_job_url,omitempty"`
	LatestFailingJobID  int64      `json:"-"`
}

// Recurrence links an open incident to the previous time the check broke.
type Recurrence struct {
	TimesBrokenInWindow  int        `json:"times_broken_in_window"`
	PreviousStartedAt    time.Time  `json:"previous_started_at"`
	PreviousBrokenBy     CommitRef  `json:"previous_broken_by"`
	PreviousFixedBy      *CommitRef `json:"previous_fixed_by,omitempty"`
	PreviousFailingSteps []string   `json:"previous_failing_steps,omitempty"`
	SameFailingStep      bool       `json:"same_failing_step"`
	Hint                 string     `json:"hint"`
}

// CheckHealth summarizes the history of a single check (workflow / job).
type CheckHealth struct {
	Check          string      `json:"check"`
	Workflow       string      `json:"workflow"`
	Job            string      `json:"job"`
	State          string      `json:"state"` // failing, flaky, passing
	Runs           int         `json:"runs"`
	Passes         int         `json:"passes"`
	Failures       int         `json:"failures"`
	Cancelled      int         `json:"cancelled,omitempty"`
	FailureRate    float64     `json:"failure_rate"`
	Timeline       string      `json:"timeline"` // oldest→newest per commit: P pass, F fail, ~ flaky (pass+fail on same commit)
	FlakyCommits   int         `json:"flaky_commits"`
	Flips          int         `json:"flips"`
	LastPassAt     *time.Time  `json:"last_pass_at,omitempty"`
	LastFailAt     *time.Time  `json:"last_fail_at,omitempty"`
	MTTRMinutes    float64     `json:"mttr_minutes,omitempty"`
	IncidentCount  int         `json:"incident_count"`
	Recurrence     *Recurrence `json:"recurrence,omitempty"`
	Incidents      []Incident  `json:"incidents,omitempty"`
	openIncidentIx int
}

// WorkflowHealth aggregates run-level metrics for a workflow.
type WorkflowHealth struct {
	Workflow          string    `json:"workflow"`
	Runs              int       `json:"runs"`
	Successes         int       `json:"successes"`
	Failures          int       `json:"failures"`
	Cancelled         int       `json:"cancelled"`
	SuccessRate       float64   `json:"success_rate"`
	MedianDurationSec float64   `json:"median_duration_sec"`
	P90DurationSec    float64   `json:"p90_duration_sec"`
	LastConclusion    string    `json:"last_conclusion"`
	LastRunAt         time.Time `json:"last_run_at"`
	LastRunURL        string    `json:"last_run_url"`
}

// FailingNow is an action-oriented digest of a check that is red right now.
type FailingNow struct {
	Check               string      `json:"check"`
	Since               time.Time   `json:"since"`
	FailingForMinutes   float64     `json:"failing_for_minutes"`
	FailingCommits      int         `json:"failing_commits"`
	StartedBeforeWindow bool        `json:"started_before_window,omitempty"`
	LastPassingCommit   *CommitRef  `json:"last_passing_commit,omitempty"`
	FirstFailingCommit  CommitRef   `json:"first_failing_commit"`
	FailingSteps        []string    `json:"failing_steps,omitempty"`
	FailingJobURL       string      `json:"failing_job_url,omitempty"`
	Flaky               bool        `json:"flaky"`
	Recurrence          *Recurrence `json:"recurrence,omitempty"`
	SuspectCommits      []CommitRef `json:"suspect_commits,omitempty"`
	SuspectFiles        []string    `json:"suspect_files,omitempty"`
	ErrorExcerpt        []string    `json:"error_excerpt,omitempty"`
	NextSteps           []string    `json:"next_steps"`
	latestFailingJobID  int64
}

// LatestFailingJobID exposes the job to fetch logs for.
func (f *FailingNow) LatestFailingJobID() int64 { return f.latestFailingJobID }

// BreakingCommit groups currently failing checks by the commit that broke them,
// so several red checks with one root cause read as a single problem.
type BreakingCommit struct {
	Commit            CommitRef  `json:"commit"`
	LastPassingCommit *CommitRef `json:"last_passing_commit,omitempty"`
	Checks            []string   `json:"checks"`
}

// Summary holds global CI health metrics.
type Summary struct {
	Health               string     `json:"health"`        // healthy, degraded, broken
	BranchStatus         string     `json:"branch_status"` // green, red, pending, unknown
	RunsAnalyzed         int        `json:"runs_analyzed"`
	CommitsAnalyzed      int        `json:"commits_analyzed"`
	ChecksTracked        int        `json:"checks_tracked"`
	RunSuccessRate       float64    `json:"run_success_rate"`
	CommitGreenRate      float64    `json:"commit_green_rate"`
	FailingChecks        int        `json:"failing_checks"`
	FlakyChecks          int        `json:"flaky_checks"`
	RecurringFailures    int        `json:"recurring_failures"`
	IncidentsInWindow    int        `json:"incidents_in_window"`
	MTTRMinutes          float64    `json:"mttr_minutes"`
	LastGreenCommit      *CommitRef `json:"last_green_commit,omitempty"`
	RedForMinutes        float64    `json:"red_for_minutes,omitempty"`
	MedianRunDurationSec float64    `json:"median_run_duration_sec"`
	InFlightRuns         int        `json:"in_flight_runs"`
	WindowStart          time.Time  `json:"window_start"`
	WindowEnd            time.Time  `json:"window_end"`
	Verdict              string     `json:"verdict"`
}

// Report is the full output of `monoflow ci health`.
type Report struct {
	Branch      string           `json:"branch"`
	GeneratedAt time.Time        `json:"generated_at"`
	Summary     Summary          `json:"summary"`
	BrokenBy    []BreakingCommit `json:"breaking_commits,omitempty"`
	FailingNow  []FailingNow     `json:"failing_now"`
	Workflows   []WorkflowHealth `json:"workflows"`
	Checks      []CheckHealth    `json:"checks"`
	InFlight    []CommitRef      `json:"in_flight,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
}

// RunData is a run plus the jobs observed for it (all attempts, oldest first).
type RunData struct {
	Run      Run
	Attempts [][]Job // index i = attempt i+1; nil when not fetched
}

// Options tunes the analysis.
type Options struct {
	Branch       string
	CheckFilter  string // case-insensitive substring; keeps full history for matches
	MaxIncidents int    // incidents kept per check when no filter (default 5)
	Now          time.Time
}

type observation struct {
	commit  int
	outcome Outcome
	at      time.Time
	job     *Job
	runURL  string
}

// Analyze builds the health report from run history.
func Analyze(data []RunData, opts Options) *Report {
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	if opts.MaxIncidents <= 0 {
		opts.MaxIncidents = 5
	}
	rep := &Report{Branch: opts.Branch, GeneratedAt: opts.Now, FailingNow: []FailingNow{}}

	sort.SliceStable(data, func(i, j int) bool { return data[i].Run.CreatedAt.Before(data[j].Run.CreatedAt) })

	// Commit ordering by first appearance.
	commitIx := map[string]int{}
	var commits []CommitRef
	for _, d := range data {
		r := d.Run
		if _, ok := commitIx[r.HeadSHA]; ok {
			continue
		}
		commitIx[r.HeadSHA] = len(commits)
		commits = append(commits, CommitRef{
			SHA: r.HeadSHA, Short: short(r.HeadSHA), Message: firstLine(r.HeadCommit.Message),
			Author: r.HeadCommit.Author.Name, Time: r.CreatedAt, RunURL: r.HTMLURL,
		})
	}

	// Job names known per workflow, used to infer passes for green runs whose jobs were not fetched.
	knownJobs := map[string]map[string]bool{}
	for _, d := range data {
		for _, jobs := range d.Attempts {
			for _, j := range jobs {
				if knownJobs[d.Run.Name] == nil {
					knownJobs[d.Run.Name] = map[string]bool{}
				}
				knownJobs[d.Run.Name][j.Name] = true
			}
		}
	}

	obs := map[string][]observation{}
	checkMeta := map[string][2]string{}
	add := func(wf, job string, o observation) {
		key := wf
		if job != "" {
			key = wf + " / " + job
		}
		checkMeta[key] = [2]string{wf, job}
		obs[key] = append(obs[key], o)
	}

	wfStats := map[string]*WorkflowHealth{}
	wfDur := map[string][]float64{}
	var allDur []float64
	inFlightSeen := map[string]bool{}

	for _, d := range data {
		r := d.Run
		ci := commitIx[r.HeadSHA]
		if r.Status != "completed" {
			if !inFlightSeen[r.HeadSHA] {
				inFlightSeen[r.HeadSHA] = true
				c := commits[ci]
				c.RunURL = r.HTMLURL
				rep.InFlight = append(rep.InFlight, c)
			}
			continue
		}

		ws := wfStats[r.Name]
		if ws == nil {
			ws = &WorkflowHealth{Workflow: r.Name}
			wfStats[r.Name] = ws
		}
		ws.Runs++
		switch outcomeOf(r.Conclusion) {
		case OutcomePass:
			ws.Successes++
		case OutcomeFail:
			ws.Failures++
		case OutcomeCancelled:
			ws.Cancelled++
		}
		ws.LastConclusion, ws.LastRunAt, ws.LastRunURL = r.Conclusion, r.CreatedAt, r.HTMLURL
		start := r.RunStartedAt
		if start.IsZero() {
			start = r.CreatedAt
		}
		if dur := r.UpdatedAt.Sub(start).Seconds(); dur > 0 {
			wfDur[r.Name] = append(wfDur[r.Name], dur)
			allDur = append(allDur, dur)
		}

		fetched := false
		for ai, jobs := range d.Attempts {
			if jobs == nil {
				continue
			}
			fetched = fetched || ai == len(d.Attempts)-1
			for i := range jobs {
				j := jobs[i]
				out := jobOutcome(&j)
				if out == OutcomeSkipped {
					continue
				}
				at := j.CompletedAt
				if at.IsZero() {
					at = r.UpdatedAt
				}
				add(r.Name, j.Name, observation{commit: ci, outcome: out, at: at, job: &j, runURL: r.HTMLURL})
			}
		}
		if fetched {
			continue
		}
		out := outcomeOf(r.Conclusion)
		if out == OutcomeSkipped {
			continue
		}
		if out == OutcomePass && len(knownJobs[r.Name]) > 0 {
			for name := range knownJobs[r.Name] {
				add(r.Name, name, observation{commit: ci, outcome: OutcomePass, at: r.UpdatedAt, runURL: r.HTMLURL})
			}
			continue
		}
		if len(knownJobs[r.Name]) == 0 {
			add(r.Name, "", observation{commit: ci, outcome: out, at: r.UpdatedAt, runURL: r.HTMLURL})
		} else if out != OutcomePass {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("jobs unavailable for %s run %d; its failure is not attributed to a check", r.Name, r.ID))
		}
	}

	// Per-check analysis.
	for key, list := range obs {
		meta := checkMeta[key]
		ch := analyzeCheck(key, meta[0], meta[1], list, commits, opts.Now)
		rep.Checks = append(rep.Checks, ch)
	}

	stateRank := map[string]int{"failing": 0, "flaky": 1, "passing": 2}
	sort.Slice(rep.Checks, func(i, j int) bool {
		a, b := rep.Checks[i], rep.Checks[j]
		if stateRank[a.State] != stateRank[b.State] {
			return stateRank[a.State] < stateRank[b.State]
		}
		if a.Failures != b.Failures {
			return a.Failures > b.Failures
		}
		return a.Check < b.Check
	})

	// Failing-now digests.
	for i := range rep.Checks {
		ch := &rep.Checks[i]
		if ch.openIncidentIx < 0 {
			continue
		}
		inc := ch.Incidents[ch.openIncidentIx]
		fn := FailingNow{
			Check:               ch.Check,
			Since:               inc.StartedAt,
			FailingForMinutes:   round1(opts.Now.Sub(inc.StartedAt).Minutes()),
			FailingCommits:      inc.FailingCommits,
			StartedBeforeWindow: inc.StartedBeforeWindow,
			LastPassingCommit:   inc.LastPassingCommit,
			FirstFailingCommit:  inc.FirstFailingCommit,
			FailingSteps:        inc.FailingSteps,
			FailingJobURL:       inc.FailingJobURL,
			Flaky:               ch.FlakyCommits > 0,
			Recurrence:          ch.Recurrence,
			latestFailingJobID:  inc.LatestFailingJobID,
		}
		fn.NextSteps = nextSteps(ch, &fn)
		rep.FailingNow = append(rep.FailingNow, fn)
	}

	// Workflows.
	for name, ws := range wfStats {
		if denom := ws.Successes + ws.Failures; denom > 0 {
			ws.SuccessRate = round3(float64(ws.Successes) / float64(denom))
		}
		ws.MedianDurationSec = round1(percentile(wfDur[name], 0.5))
		ws.P90DurationSec = round1(percentile(wfDur[name], 0.9))
		rep.Workflows = append(rep.Workflows, *ws)
	}
	sort.Slice(rep.Workflows, func(i, j int) bool {
		if rep.Workflows[i].SuccessRate != rep.Workflows[j].SuccessRate {
			return rep.Workflows[i].SuccessRate < rep.Workflows[j].SuccessRate
		}
		return rep.Workflows[i].Workflow < rep.Workflows[j].Workflow
	})

	brokenIx := map[string]int{}
	for _, f := range rep.FailingNow {
		if f.StartedBeforeWindow {
			continue
		}
		i, ok := brokenIx[f.FirstFailingCommit.SHA]
		if !ok {
			i = len(rep.BrokenBy)
			brokenIx[f.FirstFailingCommit.SHA] = i
			rep.BrokenBy = append(rep.BrokenBy, BreakingCommit{Commit: f.FirstFailingCommit, LastPassingCommit: f.LastPassingCommit})
		}
		rep.BrokenBy[i].Checks = append(rep.BrokenBy[i].Checks, f.Check)
	}

	// Global metrics are computed before any --check filtering.
	rep.Summary = summarize(rep, data, obs, commits, allDur, opts.Now)

	// Trim incident history unless a specific check was requested.
	filter := strings.ToLower(strings.TrimSpace(opts.CheckFilter))
	if filter != "" {
		var kept []CheckHealth
		for _, c := range rep.Checks {
			if strings.Contains(strings.ToLower(c.Check), filter) {
				kept = append(kept, c)
			}
		}
		rep.Checks = kept
		var keptFN []FailingNow
		for _, f := range rep.FailingNow {
			if strings.Contains(strings.ToLower(f.Check), filter) {
				keptFN = append(keptFN, f)
			}
		}
		rep.FailingNow = keptFN
		if rep.FailingNow == nil {
			rep.FailingNow = []FailingNow{}
		}
	} else {
		for i := range rep.Checks {
			if n := len(rep.Checks[i].Incidents); n > opts.MaxIncidents {
				rep.Checks[i].Incidents = rep.Checks[i].Incidents[n-opts.MaxIncidents:]
			}
		}
	}

	return rep
}

func analyzeCheck(key, wf, job string, list []observation, commits []CommitRef, now time.Time) CheckHealth {
	ch := CheckHealth{Check: key, Workflow: wf, Job: job, openIncidentIx: -1}

	// Collapse observations per commit: latest outcome wins, mixed outcomes mark flakiness.
	type commitState struct {
		final      observation
		sawPass    bool
		sawFail    bool
		failingJob *Job
	}
	per := map[int]*commitState{}
	var order []int
	for _, o := range list {
		ch.Runs++
		switch o.outcome {
		case OutcomePass:
			ch.Passes++
			t := o.at
			if ch.LastPassAt == nil || t.After(*ch.LastPassAt) {
				ch.LastPassAt = &t
			}
		case OutcomeFail:
			ch.Failures++
			t := o.at
			if ch.LastFailAt == nil || t.After(*ch.LastFailAt) {
				ch.LastFailAt = &t
			}
		case OutcomeCancelled:
			ch.Cancelled++
			continue
		}
		cs := per[o.commit]
		if cs == nil {
			cs = &commitState{}
			per[o.commit] = cs
			order = append(order, o.commit)
		}
		if o.outcome == OutcomePass {
			cs.sawPass = true
		} else {
			cs.sawFail = true
			if o.job != nil {
				cs.failingJob = o.job
			}
		}
		if cs.final.at.IsZero() || !o.at.Before(cs.final.at) {
			cs.final = o
		}
	}
	sort.Ints(order)
	if denom := ch.Passes + ch.Failures; denom > 0 {
		ch.FailureRate = round3(float64(ch.Failures) / float64(denom))
	}

	var tl strings.Builder
	var prev Outcome
	var lastPass *CommitRef
	var cur *Incident
	for idx, ci := range order {
		cs := per[ci]
		flaky := cs.sawPass && cs.sawFail
		if flaky {
			ch.FlakyCommits++
			tl.WriteByte('~')
		} else if cs.final.outcome == OutcomePass {
			tl.WriteByte('P')
		} else {
			tl.WriteByte('F')
		}
		out := cs.final.outcome
		if idx > 0 && out != prev {
			ch.Flips++
		}
		prev = out

		c := commits[ci]
		c.RunURL = cs.final.runURL
		if out == OutcomeFail {
			if cur == nil {
				cur = &Incident{
					Open:                true,
					StartedAt:           cs.final.at,
					FirstFailingCommit:  c,
					StartedBeforeWindow: idx == 0,
				}
				if lastPass != nil {
					lp := *lastPass
					cur.LastPassingCommit = &lp
				}
			}
			cur.FailingCommits++
			cur.LatestFailingCommit = c
			if cs.failingJob != nil {
				cur.FailingSteps = mergeSteps(cur.FailingSteps, failingSteps(cs.failingJob))
				cur.FailingJobURL = cs.failingJob.HTMLURL
				cur.LatestFailingJobID = cs.failingJob.ID
			}
		} else {
			if cur != nil {
				end := cs.final.at
				cur.Open = false
				cur.EndedAt = &end
				cur.DurationMinutes = round1(end.Sub(cur.StartedAt).Minutes())
				fixed := c
				cur.FixedByCommit = &fixed
				ch.Incidents = append(ch.Incidents, *cur)
				cur = nil
			}
			lp := c
			lastPass = &lp
		}
	}
	if cur != nil {
		cur.DurationMinutes = round1(now.Sub(cur.StartedAt).Minutes())
		ch.Incidents = append(ch.Incidents, *cur)
		ch.openIncidentIx = len(ch.Incidents) - 1
	}
	ch.Timeline = tl.String()
	ch.IncidentCount = len(ch.Incidents)

	var total float64
	var closed int
	for _, inc := range ch.Incidents {
		if !inc.Open && !inc.StartedBeforeWindow {
			total += inc.DurationMinutes
			closed++
		}
	}
	if closed > 0 {
		ch.MTTRMinutes = round1(total / float64(closed))
	}

	switch {
	case ch.openIncidentIx >= 0:
		ch.State = "failing"
	case ch.FlakyCommits > 0 || (len(order) >= 4 && ch.IncidentCount >= 2 && float64(ch.Flips)/float64(len(order)-1) >= 0.4):
		ch.State = "flaky"
	default:
		ch.State = "passing"
	}

	if ch.openIncidentIx > 0 {
		open := ch.Incidents[ch.openIncidentIx]
		prevInc := ch.Incidents[ch.openIncidentIx-1]
		rec := &Recurrence{
			TimesBrokenInWindow:  ch.IncidentCount,
			PreviousStartedAt:    prevInc.StartedAt,
			PreviousBrokenBy:     prevInc.FirstFailingCommit,
			PreviousFixedBy:      prevInc.FixedByCommit,
			PreviousFailingSteps: prevInc.FailingSteps,
			SameFailingStep:      overlaps(open.FailingSteps, prevInc.FailingSteps),
		}
		if rec.SameFailingStep && rec.PreviousFixedBy != nil {
			rec.Hint = fmt.Sprintf("Same step failed before and was fixed by %s (%q); check whether that fix was reverted or bypassed: git show %s",
				rec.PreviousFixedBy.Short, rec.PreviousFixedBy.Message, rec.PreviousFixedBy.Short)
		} else if rec.PreviousFixedBy != nil {
			rec.Hint = fmt.Sprintf("Check broke %d times in window; previous fix was %s (%q) but the failing step differs, likely a new root cause",
				rec.TimesBrokenInWindow, rec.PreviousFixedBy.Short, rec.PreviousFixedBy.Message)
		}
		ch.Recurrence = rec
	}
	return ch
}

func nextSteps(ch *CheckHealth, fn *FailingNow) []string {
	var steps []string
	switch {
	case fn.Flaky:
		steps = append(steps, "Check passed and failed on the same commit: treat as flaky, inspect for races/timeouts before bisecting code")
	case fn.LastPassingCommit != nil:
		steps = append(steps,
			fmt.Sprintf("Inspect the breaking range: git log --oneline %s..%s && git diff --stat %s %s",
				fn.LastPassingCommit.Short, fn.FirstFailingCommit.Short, fn.LastPassingCommit.Short, fn.FirstFailingCommit.Short))
	case fn.StartedBeforeWindow:
		steps = append(steps, "Failure predates the analyzed window: re-run with a larger --limit to find the breaking commit")
	}
	if fn.Recurrence != nil && fn.Recurrence.PreviousFixedBy != nil {
		steps = append(steps, fmt.Sprintf("Review the previous fix: git show %s", fn.Recurrence.PreviousFixedBy.Short))
	}
	if fn.FailingJobURL != "" && len(fn.ErrorExcerpt) == 0 {
		steps = append(steps, fmt.Sprintf("Fetch the error: monoflow ci health --check=%q --logs", ch.Check))
	}
	steps = append(steps, "Reproduce locally: monoflow verify --affected")
	return steps
}

func summarize(rep *Report, data []RunData, obs map[string][]observation, commits []CommitRef, allDur []float64, now time.Time) Summary {
	s := Summary{
		CommitsAnalyzed:      len(commits),
		ChecksTracked:        len(rep.Checks),
		InFlightRuns:         0,
		MedianRunDurationSec: round1(percentile(allDur, 0.5)),
		WindowEnd:            now,
	}
	var succ, fail int
	for _, d := range data {
		if d.Run.Status != "completed" {
			s.InFlightRuns++
			continue
		}
		s.RunsAnalyzed++
		switch outcomeOf(d.Run.Conclusion) {
		case OutcomePass:
			succ++
		case OutcomeFail:
			fail++
		}
	}
	if len(data) > 0 {
		s.WindowStart = data[0].Run.CreatedAt
	}
	if succ+fail > 0 {
		s.RunSuccessRate = round3(float64(succ) / float64(succ+fail))
	}

	// Commit greenness: a commit is green when every check observed on it ends in pass.
	commitFail := make([]int, len(commits)) // -1 unknown, 0 green, 1 red
	for i := range commitFail {
		commitFail[i] = -1
	}
	lastOut := map[string]map[int]observation{}
	for key, list := range obs {
		lastOut[key] = map[int]observation{}
		for _, o := range list {
			if o.outcome != OutcomePass && o.outcome != OutcomeFail {
				continue
			}
			if p, ok := lastOut[key][o.commit]; !ok || !o.at.Before(p.at) {
				lastOut[key][o.commit] = o
			}
		}
	}
	for _, m := range lastOut {
		for ci, o := range m {
			if o.outcome == OutcomeFail {
				commitFail[ci] = 1
			} else if commitFail[ci] == -1 {
				commitFail[ci] = 0
			}
		}
	}
	var green, known int
	for ci, st := range commitFail {
		if st == -1 {
			continue
		}
		known++
		if st == 0 {
			green++
			c := commits[ci]
			s.LastGreenCommit = &c
		}
	}
	if known > 0 {
		s.CommitGreenRate = round3(float64(green) / float64(known))
	}

	var mttrSum float64
	var mttrN int
	for _, c := range rep.Checks {
		s.IncidentsInWindow += c.IncidentCount
		switch c.State {
		case "failing":
			s.FailingChecks++
		case "flaky":
			s.FlakyChecks++
		}
		if c.Recurrence != nil {
			s.RecurringFailures++
		}
		for _, inc := range c.Incidents {
			if !inc.Open && !inc.StartedBeforeWindow {
				mttrSum += inc.DurationMinutes
				mttrN++
			}
		}
	}
	if mttrN > 0 {
		s.MTTRMinutes = round1(mttrSum / float64(mttrN))
	}
	if len(rep.FailingNow) > 0 {
		earliest := rep.FailingNow[0].Since
		for _, f := range rep.FailingNow {
			if f.Since.Before(earliest) {
				earliest = f.Since
			}
		}
		s.RedForMinutes = round1(now.Sub(earliest).Minutes())
	}

	switch {
	case len(data) == 0:
		s.BranchStatus = "unknown"
	case s.FailingChecks > 0:
		s.BranchStatus = "red"
	case s.InFlightRuns > 0 && s.RunsAnalyzed == 0:
		s.BranchStatus = "pending"
	default:
		s.BranchStatus = "green"
	}

	switch {
	case s.FailingChecks > 0:
		s.Health = "broken"
	case s.FlakyChecks > 0 || (s.RunsAnalyzed > 0 && s.RunSuccessRate < 0.8):
		s.Health = "degraded"
	case len(data) == 0:
		s.Health = "unknown"
	default:
		s.Health = "healthy"
	}

	switch s.Health {
	case "broken":
		names := make([]string, 0, len(rep.FailingNow))
		for _, f := range rep.FailingNow {
			names = append(names, f.Check)
		}
		s.Verdict = fmt.Sprintf("%d check(s) failing on %s: %s. See failing_now for the breaking commit range and next steps.",
			s.FailingChecks, rep.Branch, strings.Join(names, ", "))
	case "degraded":
		s.Verdict = fmt.Sprintf("Branch is green but unstable: %d flaky check(s), run success rate %.0f%%.", s.FlakyChecks, s.RunSuccessRate*100)
	case "healthy":
		s.Verdict = "All tracked checks are passing."
	default:
		s.Verdict = "No workflow runs found for this branch."
	}
	return s
}

func failingSteps(j *Job) []string {
	var out []string
	for _, st := range j.Steps {
		if outcomeOf(st.Conclusion) == OutcomeFail {
			out = append(out, st.Name)
		}
	}
	return out
}

func mergeSteps(a, b []string) []string {
	for _, s := range b {
		found := false
		for _, x := range a {
			if x == s {
				found = true
				break
			}
		}
		if !found {
			a = append(a, s)
		}
	}
	return a
}

func overlaps(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	idx := int(p * float64(len(c)-1))
	return c[idx]
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
