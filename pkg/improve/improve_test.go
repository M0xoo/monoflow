package improve

import (
	"strings"
	"testing"
	"time"
)

func TestSessionRetroAndClose(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	wt := "/repo/.agents/worktrees/a"
	for _, e := range []Entry{
		{Kind: "cmd", Cmd: "start a", Worktree: wt},
		{Kind: "cmd", Cmd: "verify", Exit: 1, Ms: 3000, Worktree: wt},
		{Kind: "redirect", Cmd: "go test ./...", Worktree: wt},
		{Kind: "cmd", Cmd: "verify", Ms: 90000, Worktree: wt},
		{Kind: "cmd", Cmd: "verify", Worktree: "/other"},
	} {
		if err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	sess := s.Current(wt)
	if sess.Commands() != 3 || !sess.Worked() {
		t.Fatalf("session: %d commands, worked=%v", sess.Commands(), sess.Worked())
	}
	text := RetroText(sess, nil)
	for _, want := range []string{"3 monoflow commands", "verify 2 (1 failed)", "verify (exit 1)", "go test ./...", "slow: verify 90s", "improve add", "improve none"} {
		if !strings.Contains(text, want) {
			t.Errorf("retro text lacks %q:\n%s", want, text)
		}
	}
	if !s.ClaimNudge(sess) || s.ClaimNudge(sess) {
		t.Fatal("a session is nudged exactly once")
	}
	sg, err := s.Add(Suggestion{Title: "verify should name the pending dependents", Area: "verify", Worktree: wt})
	if err != nil || sg.Session != sess.ID || !strings.HasPrefix(sg.ID, "s") {
		t.Fatalf("add: %+v %v", sg, err)
	}
	if len(s.Current(wt).Filed) != 1 {
		t.Fatal("filed suggestion belongs to the open session")
	}
	r, err := s.Close(wt, "agent/a", "a", "", false)
	if err != nil || r.Suggestions != 1 || r.Commands != 3 {
		t.Fatalf("close: %+v %v", r, err)
	}
	if after := s.Current(wt); len(after.Entries) != 0 || after.Worked() || s.ClaimNudge(after) {
		t.Fatal("a closed session starts a new, empty one and is not nudged")
	}
}

func TestTriageClustersAndMarks(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	add := func(title, area, sev, wt string) Suggestion {
		sg, err := s.Add(Suggestion{Title: title, Area: area, Severity: sev, Worktree: wt})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
		return sg
	}
	a := add("verify should list the pending dependents", "verify", "high", "w1")
	add("Verify should list which dependents are pending", "verify", "medium", "w2")
	c := add("ship output needs the PR url", "ship", "low", "w3")
	d := add("unrelated: help for graph impact is unclear", "docs", "", "w1")
	_, _ = s.Add(Suggestion{Title: "x", Detail: "same-as:" + c.ID, Worktree: "w4"})

	rep := s.Triage(time.Time{})
	if rep.Suggestions != 5 || len(rep.Open) != 3 {
		t.Fatalf("want 5 suggestions in 3 clusters, got %d / %d:\n%s", rep.Suggestions, len(rep.Open), rep.Text())
	}
	if top := rep.Open[0]; len(top.Members) != 2 || top.Sessions != 2 || top.Score != 6 {
		t.Fatalf("top cluster = the verify one, 2 sessions × high: %+v", top)
	}
	marked, err := s.Mark([]string{a.ID}, "fixed", "done in #28", false)
	if err != nil || len(marked) != 2 {
		t.Fatalf("mark marks the whole cluster: %v %v", marked, err)
	}
	if _, err := s.Mark([]string{d.ID}, "bogus", "", false); err == nil {
		t.Fatal("unknown status must be refused")
	}
	rep = s.Triage(time.Time{})
	if len(rep.Open) != 2 || rep.Decided["fixed"] != 2 {
		t.Fatalf("after mark: %s", rep.Text())
	}
	// A new report of a fixed problem reopens it.
	add("verify should list pending dependents", "verify", "high", "w5")
	if rep = s.Triage(time.Time{}); len(rep.Open) != 3 {
		t.Fatalf("regression must reopen the cluster:\n%s", rep.Text())
	}
}
