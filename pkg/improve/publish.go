package improve

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultIssueRepo is where valid suggestions become issues: MonoFlow's own repository.
const DefaultIssueRepo = "M0xoo/monoflow"

// autoNote prefixes the triage decisions Publish records (as opposed to a maintainer's).
const autoNote = "auto-triage: "

// issueMarker tags issues filed by `monoflow improve`, so later sessions find them again.
const issueMarker = "<!-- monoflow-improve -->"

// Issue is an issue in the tracker.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	URL    string `json:"url"`
	Body   string `json:"body"`
}

// Tracker is the issue tracker suggestions are published to (GitHub through gh).
type Tracker interface {
	List() ([]Issue, error)
	Create(title, body string, labels []string) (Issue, error)
	Comment(number int, body string) error
}

// Published records what triage did with one suggestion.
type Published struct {
	Action   string    `json:"action"` // created | commented | rejected | pending
	Issue    int       `json:"issue,omitempty"`
	URL      string    `json:"url,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	TS       time.Time `json:"ts"`
	Attempts int       `json:"attempts,omitempty"`
}

// Done reports whether triage is finished with the suggestion (pending ones are retried).
func (p Published) Done() bool { return p.Action != "" && p.Action != "pending" }

// Publications returns the triage outcome by suggestion id.
func (s *Store) Publications() map[string]Published {
	out := map[string]Published{}
	if data, err := os.ReadFile(s.path("issues.json")); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (s *Store) savePublications(p map[string]Published) error {
	mu.Lock()
	defer mu.Unlock()
	if err := s.mkdir(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(s.path("issues.json"), append(data, '\n'), 0644)
}

// knownAreas are the --area values that name something in MonoFlow.
var knownAreas = map[string]bool{}

func init() {
	for _, a := range strings.Fields(`start find graph verify ship commit push sync branch events ci outer worktree pr
		review queue quarantine version task guard subset cache deliver init hook hooks improve checkpoint clone docs
		install plugin agents config`) {
		knownAreas[a] = true
	}
}

// monoflowWords: evidence names MonoFlow, or starts with one of its commands ("verify -> FAIL").
var monoflowWords = regexp.MustCompile(`(?i)\bmonoflow\b|^\s*\$?\s*(start|find|graph|verify|ship|commit|push|sync|events|outer|worktree|pr|improve|checkpoint|hook|init|ci)\b`)

// Validate is the automatic triage of one suggestion: is it a specific, evidenced problem in
// MonoFlow that nobody already declined? It returns "" when valid, else why not.
func Validate(sg Suggestion, cluster []Suggestion, dec map[string]Decision) string {
	if len(tokens(sg.Title)) < 3 {
		return "title too vague: say what MonoFlow should do (at least a few words)"
	}
	ev := strings.TrimSpace(sg.Evidence)
	if len(ev) < 15 {
		return "no evidence: add the exact monoflow command and an output snippet (--evidence=...)"
	}
	if !monoflowWords.MatchString(ev) {
		return "the evidence shows no MonoFlow command: retros are about MonoFlow, not about the task's code"
	}
	if sg.Area != "" && !knownAreas[strings.Fields(strings.ToLower(sg.Area))[0]] {
		return fmt.Sprintf("unknown --area=%s (use the monoflow command involved, or docs)", sg.Area)
	}
	for _, m := range cluster {
		// Only a maintainer's verdict closes a problem; auto-triage rejections (a missing
		// evidence, say) must not stop a better-filed report of the same thing.
		if d, ok := dec[m.ID]; ok && (d.Status == "wontfix" || d.Status == "invalid") && m.ID != sg.ID && !strings.HasPrefix(d.Note, autoNote) {
			reason := "same as " + m.ID + ", marked " + d.Status
			if d.Note != "" {
				reason += ": " + d.Note
			}
			return reason
		}
	}
	return ""
}

// PublishOptions configures Publish.
type PublishOptions struct {
	Tracker Tracker
	Version string // MonoFlow build that saw the problem
	Context string // e.g. "go, javascript · darwin/arm64"
	Redact  func(string) string
	DryRun  bool
	Only    map[string]bool // limit to these suggestion ids (nil: every unpublished one)
}

// Outcome is Publish's result for one suggestion.
type Outcome struct {
	Suggestion Suggestion
	Published
	Title, Body string // what was (or, in a dry run, would be) filed
}

// Publish triages every suggestion not handled yet (or retried: pending): invalid ones are
// marked invalid with the reason; valid ones are filed as an issue, or added as a comment on
// the issue already open for the same problem (filed by this store or by any other user's).
func (s *Store) Publish(o PublishOptions) ([]Outcome, error) {
	if o.Redact == nil {
		o.Redact = func(x string) string { return x }
	}
	all := s.Suggestions()
	pubs := s.Publications()
	dec := s.Decisions()
	clusterOf := map[string][]Suggestion{}
	for _, c := range Cluster(all) {
		for _, m := range c.Members {
			clusterOf[m.ID] = c.Members
		}
	}
	var todo []Suggestion
	for _, sg := range all {
		if (o.Only == nil || o.Only[sg.ID]) && !pubs[sg.ID].Done() {
			if d, ok := dec[sg.ID]; ok && !Open(d.Status) && !strings.HasPrefix(d.Note, autoNote) {
				continue // a maintainer decided already
			}
			todo = append(todo, sg)
		}
	}
	if len(todo) == 0 {
		return nil, nil
	}
	var remote []Issue
	var listErr error
	listed := false
	var out []Outcome
	for _, sg := range todo {
		p := pubs[sg.ID]
		p.TS = time.Now().UTC()
		if reason := Validate(sg, clusterOf[sg.ID], dec); reason != "" {
			p.Action, p.Reason = "rejected", reason
			if !o.DryRun {
				pubs[sg.ID] = p
				_, _ = s.Mark([]string{sg.ID}, "invalid", autoNote+reason, true)
			}
			out = append(out, Outcome{Suggestion: sg, Published: p})
			continue
		}
		// The same problem may already have an issue: from this store's cluster, or in the tracker.
		target := 0
		url := ""
		for _, m := range clusterOf[sg.ID] {
			if q := pubs[m.ID]; q.Issue > 0 {
				target, url = q.Issue, q.URL
			}
		}
		if target == 0 && o.Tracker != nil {
			if !listed {
				remote, listErr = o.Tracker.List()
				listed = true
			}
			if listErr == nil {
				if is := matchIssue(sg, remote); is != nil {
					target, url = is.Number, is.URL
				}
			}
		}
		title, body := IssueTitle(sg), issueBody(sg, len(clusterOf[sg.ID]), o)
		if target > 0 {
			title, body = "", commentBody(sg, o)
		}
		if o.DryRun {
			p.Action = "created"
			if target > 0 {
				p.Action, p.Issue, p.URL = "commented", target, url
			}
			out = append(out, Outcome{sg, p, title, body})
			continue
		}
		var err error
		switch {
		case o.Tracker == nil:
			err = fmt.Errorf("no issue tracker")
		case listErr != nil:
			err = listErr
		case target > 0:
			err = o.Tracker.Comment(target, body)
			if err == nil {
				p.Action, p.Issue, p.URL = "commented", target, url
			}
		default:
			var is Issue
			is, err = o.Tracker.Create(title, body, labelsFor(sg))
			if err == nil {
				p.Action, p.Issue, p.URL = "created", is.Number, is.URL
				remote = append(remote, is)
			}
		}
		if err != nil {
			p.Action, p.Reason = "pending", err.Error()
			p.Attempts++
		} else {
			p.Reason = ""
			_, _ = s.Mark([]string{sg.ID}, "accepted", fmt.Sprintf(autoNote+"issue #%d", p.Issue), true)
		}
		pubs[sg.ID] = p
		out = append(out, Outcome{sg, p, title, body})
	}
	if !o.DryRun {
		return out, s.savePublications(pubs)
	}
	return out, nil
}

// IssueTitle is the issue title for a suggestion.
func IssueTitle(sg Suggestion) string {
	area := strings.ToLower(strings.TrimSpace(sg.Area))
	if area == "" {
		area = "general"
	}
	return oneLine(fmt.Sprintf("improve(%s): %s", area, sg.Title), 200)
}

// matchIssue finds an issue filed by monoflow improve for the same problem.
func matchIssue(sg Suggestion, issues []Issue) *Issue {
	want := tokens(sg.Title + " " + sg.Area)
	var best *Issue
	bestScore := 0.0
	for i := range issues {
		is := &issues[i]
		if !strings.Contains(is.Body, issueMarker) {
			continue
		}
		t := is.Title
		if j := strings.Index(t, "): "); strings.HasPrefix(t, "improve(") && j > 0 {
			t = t[len("improve("):j] + " " + t[j+3:]
		}
		if sc := jaccard(want, tokens(t)); sc >= 0.5 && sc > bestScore {
			best, bestScore = is, sc
		}
	}
	return best
}

func labelsFor(sg Suggestion) []string {
	switch sg.Kind {
	case "bug":
		return []string{"bug"}
	case "docs":
		return []string{"documentation"}
	}
	return []string{"enhancement"}
}

func fence(s string) string {
	return "```\n" + strings.ReplaceAll(s, "```", "'''") + "\n```"
}

func issueBody(sg Suggestion, sessions int, o PublishOptions) string {
	r := o.Redact
	var b strings.Builder
	b.WriteString(issueMarker + "\n")
	fmt.Fprintf(&b, "**What MonoFlow should do:** %s\n\n", r(sg.Title))
	var tags []string
	for _, kv := range [][2]string{{"Area", sg.Area}, {"Kind", sg.Kind}, {"Severity", sg.Severity}} {
		if kv[1] != "" {
			tags = append(tags, "**"+kv[0]+":** "+kv[1])
		}
	}
	if len(tags) > 0 {
		b.WriteString(strings.Join(tags, " · ") + "\n\n")
	}
	b.WriteString("**Evidence**\n" + fence(trim(r(sg.Evidence), 2000)) + "\n\n")
	if d := strings.TrimSpace(stripSameAs(sg.Detail)); d != "" {
		b.WriteString("**Detail**\n" + trim(r(d), 2000) + "\n\n")
	}
	fmt.Fprintf(&b, "**Seen in** %d agent session(s) · MonoFlow %s", max(sessions, 1), firstNonEmpty(o.Version, "unknown"))
	if o.Context != "" {
		b.WriteString(" · " + o.Context)
	}
	b.WriteString("\n\n_Filed by `monoflow improve` after an agent's retro, once its automatic triage passed (evidence of a MonoFlow command, not a known wontfix). Paths and secrets are redacted. Opt out: `\"improve\": {\"issues\": false}` in `.agents/monoflow.config.json`._\n")
	return b.String()
}

func commentBody(sg Suggestion, o PublishOptions) string {
	r := o.Redact
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nAnother agent session hit this (MonoFlow %s", issueMarker, firstNonEmpty(o.Version, "unknown"))
	if o.Context != "" {
		b.WriteString(" · " + o.Context)
	}
	b.WriteString("): " + r(sg.Title) + "\n\n" + fence(trim(r(sg.Evidence), 1500)) + "\n")
	if d := strings.TrimSpace(stripSameAs(sg.Detail)); d != "" {
		b.WriteString("\n" + trim(r(d), 1000) + "\n")
	}
	return b.String()
}

func stripSameAs(d string) string {
	if id := sameAs(Suggestion{Detail: d}); id != "" {
		d = strings.Replace(d, "same-as:"+id, "", 1)
	}
	return d
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// secretRes match credentials; a first group, when present, is kept (the "token=" part).
var secretRes = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
	regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})\b`),
	regexp.MustCompile(`(?i)\b((?:api[_-]?key|token|secret|password|passwd|authorization)\s*[:=]\s*)["']?[^\s"']{8,}`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/-]{16,}=*`),
	regexp.MustCompile(`(?i)(https?://)[^\s/@]+:[^\s/@]+@`),
}
var emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// Redactor scrubs what must not leave the machine: the repository's location and remote,
// the home directory, e-mail addresses and anything shaped like a credential.
func Redactor(repoRoot string) func(string) string {
	var paths [][2]string
	add := func(p, as string) {
		if p != "" && p != "/" {
			paths = append(paths, [2]string{p, as})
		}
	}
	if repoRoot != "" {
		if real, err := filepath.EvalSymlinks(repoRoot); err == nil && real != repoRoot {
			add(real, "<repo>")
		}
		add(repoRoot, "<repo>")
		if strings.HasPrefix(repoRoot, "/private/") {
			add(strings.TrimPrefix(repoRoot, "/private"), "<repo>")
		} else {
			add("/private"+repoRoot, "<repo>")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home, "~")
	}
	var remotes []string
	if repoRoot != "" {
		if out, err := exec.Command("git", "-C", repoRoot, "remote", "-v").Output(); err == nil {
			for _, f := range strings.Fields(string(out)) {
				if strings.Contains(f, "/") || strings.Contains(f, ":") {
					f = strings.TrimSuffix(f, ".git")
					if strings.Contains(f, "M0xoo/monoflow") {
						continue // MonoFlow's own repository is public
					}
					remotes = append(remotes, f)
					if i := strings.LastIndexAny(f, ":/"); i > 0 {
						if j := strings.LastIndexAny(f[:i], ":/"); j >= 0 {
							remotes = append(remotes, f[j+1:]) // owner/name
						}
					}
				}
			}
		}
	}
	return func(s string) string {
		for _, re := range secretRes {
			if re.NumSubexp() > 0 {
				s = re.ReplaceAllString(s, "${1}<redacted>")
			} else {
				s = re.ReplaceAllString(s, "<redacted>")
			}
		}
		for _, p := range paths {
			s = strings.ReplaceAll(s, p[0], p[1])
		}
		for _, r := range remotes {
			if len(r) > 3 {
				s = strings.ReplaceAll(s, r, "<origin>")
			}
		}
		return emailRe.ReplaceAllString(s, "<email>")
	}
}

// GHTracker files issues in repo with the gh CLI.
type GHTracker struct {
	Repo string
	Bin  string // default "gh"
}

func (g GHTracker) gh(args ...string) ([]byte, error) {
	bin := firstNonEmpty(g.Bin, "gh")
	cmd := exec.Command(bin, append(args, "-R", g.Repo)...)
	out, err := cmd.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return out, fmt.Errorf("gh %s: %s", args[0]+" "+args[1], oneLine(msg, 200))
	}
	return out, nil
}

// List returns the repository's issues (open and closed) that monoflow improve filed.
func (g GHTracker) List() ([]Issue, error) {
	out, err := g.gh("issue", "list", "--state", "all", "--limit", "300", "--search", "\"monoflow improve\" in:body", // the footer; the marker (an HTML comment) is checked below
		"--json", "number,title,state,url,body")
	if err != nil {
		return nil, err
	}
	var is []Issue
	return is, json.Unmarshal(out, &is)
}

// Create opens an issue; labels the filer may not apply are dropped rather than failing.
func (g GHTracker) Create(title, body string, labels []string) (Issue, error) {
	args := []string{"issue", "create", "--title", title, "--body", body}
	out, err := g.gh(append(args, "--label", strings.Join(labels, ","))...)
	if err != nil && len(labels) > 0 {
		out, err = g.gh(args...)
	}
	if err != nil {
		return Issue{}, err
	}
	url := strings.TrimSpace(string(out))
	if f := strings.Fields(url); len(f) > 0 {
		url = f[len(f)-1]
	}
	n, _ := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	return Issue{Number: n, Title: title, State: "OPEN", URL: url, Body: body}, nil
}

// Comment adds a comment to an issue.
func (g GHTracker) Comment(number int, body string) error {
	_, err := g.gh("issue", "comment", strconv.Itoa(number), "--body", body)
	return err
}
