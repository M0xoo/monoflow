package improve

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ClusterOf is a group of suggestions that ask for the same thing.
type ClusterOf struct {
	Members  []Suggestion
	Sessions int
	Score    int
	Status   string // the cluster's least-decided status: new < accepted < the rest
}

// Lead is the suggestion that names the cluster (the most detailed one).
func (c ClusterOf) Lead() Suggestion {
	best := c.Members[0]
	for _, m := range c.Members[1:] {
		if len(m.Detail)+len(m.Evidence) > len(best.Detail)+len(best.Evidence) {
			best = m
		}
	}
	return best
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the to of in on for and or is it its be by with when
		should not no does doesn't dont don't do monoflow from as at that this than then so my your
		me we our are was were has have had can cannot can't into out up via per also only all any`) {
		stopwords[w] = true
	}
}

// tokens normalizes text into comparable words (lowercase, crude stemming, no stopwords).
func tokens(s string) map[string]bool {
	out := map[string]bool{}
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	})
	for _, w := range f {
		w = strings.Trim(w, "-_")
		if len(w) < 2 || stopwords[w] {
			continue
		}
		for _, suf := range []string{"ing", "ed", "es", "s"} {
			if len(w) > len(suf)+3 && strings.HasSuffix(w, suf) {
				w = strings.TrimSuffix(w, suf)
				break
			}
		}
		out[w] = true
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// SameAs links a suggestion to an earlier one it duplicates ("+1"); stored in Detail as a
// "same-as:<id>" marker by `improve add --same-as`.
func sameAs(sg Suggestion) string {
	if i := strings.Index(sg.Detail, "same-as:"); i >= 0 {
		return strings.Fields(sg.Detail[i+len("same-as:"):] + " ")[0]
	}
	return ""
}

var severityWeight = map[string]int{"high": 3, "medium": 2, "low": 1}

// Cluster groups suggestions asking for the same thing: same-as links, or titles (with
// their area) sharing at least half of their words.
func Cluster(all []Suggestion) []ClusterOf {
	parent := make([]int, len(all))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	idx := map[string]int{}
	toks := make([]map[string]bool, len(all))
	for i, sg := range all {
		idx[sg.ID] = i
		toks[i] = tokens(sg.Title + " " + sg.Area)
	}
	for i := range all {
		if j, ok := idx[sameAs(all[i])]; ok {
			union(i, j)
		}
		for j := 0; j < i; j++ {
			if jaccard(toks[i], toks[j]) >= 0.5 {
				union(i, j)
			}
		}
	}
	groups := map[int][]Suggestion{}
	var order []int
	for i, sg := range all {
		r := find(i)
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], sg)
	}
	var out []ClusterOf
	for _, r := range order {
		c := ClusterOf{Members: groups[r]}
		sessions := map[string]bool{}
		sev := 1
		bug := 0
		for _, m := range c.Members {
			sessions[m.Session+m.Worktree] = true
			if w := severityWeight[m.Severity]; w > sev {
				sev = w
			}
			if m.Kind == "bug" {
				bug = 1
			}
		}
		c.Sessions = len(sessions)
		c.Score = c.Sessions*sev + bug
		out = append(out, c)
	}
	return out
}

// withStatus fills each cluster's status from the triage decisions: "new" while any member
// is undecided, else "accepted" while any member is accepted, else the first member's verdict.
// So a fresh report of an issue already marked fixed reopens it.
func withStatus(cs []ClusterOf, dec map[string]Decision) []ClusterOf {
	for i := range cs {
		st := ""
		for _, m := range cs[i].Members {
			d, ok := dec[m.ID]
			switch {
			case !ok || d.Status == "new":
				st = "new"
			case d.Status == "accepted" && st != "new":
				st = "accepted"
			case st == "":
				st = d.Status
			}
		}
		cs[i].Status = st
	}
	return cs
}

// Report is a triage view of the store.
type Report struct {
	Suggestions int
	Sessions    int // retros closed
	NoneRetros  int // retros that found nothing to improve
	Open        []ClusterOf
	Decided     map[string]int // status -> suggestions
	Since       time.Time
}

// Triage clusters the suggestions filed since `since` (zero: all), open clusters first by score.
func (s *Store) Triage(since time.Time) Report {
	var all []Suggestion
	for _, sg := range s.Suggestions() {
		if sg.TS.After(since) {
			all = append(all, sg)
		}
	}
	rep := Report{Suggestions: len(all), Decided: map[string]int{}, Since: since}
	for _, r := range s.Retros() {
		if r.TS.After(since) {
			rep.Sessions++
			if r.None {
				rep.NoneRetros++
			}
		}
	}
	dec := s.Decisions()
	for _, c := range withStatus(Cluster(all), dec) {
		if Open(c.Status) {
			rep.Open = append(rep.Open, c)
		}
		for _, m := range c.Members {
			if d, ok := dec[m.ID]; ok && !Open(d.Status) {
				rep.Decided[d.Status]++
			}
		}
	}
	sort.SliceStable(rep.Open, func(i, j int) bool { return rep.Open[i].Score > rep.Open[j].Score })
	return rep
}

// Text renders the report (markdown-friendly plain text).
func (rep Report) Text() string {
	var b strings.Builder
	scope := "all time"
	if !rep.Since.IsZero() {
		scope = "since " + rep.Since.Local().Format("2006-01-02 15:04")
	}
	fmt.Fprintf(&b, "MonoFlow improvement triage (%s): %d suggestions, %d retros (%d with nothing to improve)\n",
		scope, rep.Suggestions, rep.Sessions, rep.NoneRetros)
	if len(rep.Open) == 0 {
		b.WriteString("open: none\n")
	} else {
		fmt.Fprintf(&b, "open: %d\n", len(rep.Open))
	}
	for i, c := range rep.Open {
		l := c.Lead()
		var ids []string
		for _, m := range c.Members {
			ids = append(ids, m.ID)
		}
		var tags []string
		for _, t := range []string{l.Area, l.Kind, l.Severity} {
			if t != "" {
				tags = append(tags, t)
			}
		}
		tag := strings.Join(tags, " · ")
		fmt.Fprintf(&b, "\n%d. %s  [score %d, %d session(s)%s]\n", i+1, l.Title, c.Score, c.Sessions, statusTag(c.Status))
		if tag != "" {
			fmt.Fprintf(&b, "   %s · ids: %s\n", tag, strings.Join(ids, " "))
		} else {
			fmt.Fprintf(&b, "   ids: %s\n", strings.Join(ids, " "))
		}
		if d := strings.TrimSpace(l.Detail); d != "" {
			fmt.Fprintf(&b, "   detail: %s\n", oneLine(d, 400))
		}
		if e := strings.TrimSpace(l.Evidence); e != "" {
			fmt.Fprintf(&b, "   evidence: %s\n", oneLine(e, 400))
		}
		for _, m := range c.Members {
			if m.ID != l.ID && m.Title != l.Title {
				fmt.Fprintf(&b, "   also: %s (%s)\n", oneLine(m.Title, 160), m.ID)
			}
		}
	}
	if len(rep.Decided) > 0 {
		var parts []string
		for _, st := range Statuses {
			if n := rep.Decided[st]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s %d", st, n))
			}
		}
		b.WriteString("\ndecided: " + strings.Join(parts, " · ") + "\n")
	}
	if len(rep.Open) > 0 {
		b.WriteString("\nnext: monoflow improve mark <id> accepted|fixed|wontfix|invalid --note=\"...\" (marks the id's whole group)\n")
	}
	return b.String()
}

func statusTag(st string) string {
	if st == "" || st == "new" {
		return ""
	}
	return ", " + st
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
