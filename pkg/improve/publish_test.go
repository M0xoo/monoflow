package improve

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeTracker struct {
	issues   []Issue
	comments map[int][]string
	fail     error
	created  int
}

func (f *fakeTracker) List() ([]Issue, error) { return f.issues, f.fail }
func (f *fakeTracker) Create(title, body string, labels []string) (Issue, error) {
	if f.fail != nil {
		return Issue{}, f.fail
	}
	f.created++
	is := Issue{Number: 100 + len(f.issues), Title: title, Body: body, URL: fmt.Sprintf("https://x/issues/%d", 100+len(f.issues))}
	f.issues = append(f.issues, is)
	return is, nil
}
func (f *fakeTracker) Comment(n int, body string) error {
	if f.fail != nil {
		return f.fail
	}
	if f.comments == nil {
		f.comments = map[int][]string{}
	}
	f.comments[n] = append(f.comments[n], body)
	return nil
}

func add(t *testing.T, s *Store, sg Suggestion) Suggestion {
	t.Helper()
	out, err := s.Add(sg)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	return out
}

func outcome(outs []Outcome, id string) Outcome {
	for _, o := range outs {
		if o.Suggestion.ID == id {
			return o
		}
	}
	return Outcome{}
}

func TestPublishTriagesThenFilesOrComments(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	good := add(t, s, Suggestion{Title: "verify should name the pending dependents", Area: "verify", Kind: "friction", Worktree: "w1",
		Evidence: "monoflow verify -> PENDING 2/2 in /Users/alice/src/acme, ask alice@acme.io, token=abcdef1234567890"})
	vague := add(t, s, Suggestion{Title: "bad", Evidence: "monoflow verify -> FAIL something", Worktree: "w1"})
	noEv := add(t, s, Suggestion{Title: "ship should print the PR url", Area: "ship", Worktree: "w1"})
	taskCode := add(t, s, Suggestion{Title: "add a quickfix for unused Sprintf results", Worktree: "w1", Evidence: "ledger.go:24 result of fmt.Sprintf call not used"})
	tr := &fakeTracker{}
	redact := func(x string) string {
		return strings.NewReplacer("/Users/alice/src/acme", "<repo>").Replace(Redactor("")(x))
	}
	outs, err := s.Publish(PublishOptions{Tracker: tr, Version: "abc1234", Redact: redact})
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(outs, good.ID); o.Action != "created" || o.Issue != 100 {
		t.Fatalf("valid suggestion must be filed: %+v", o)
	}
	for id, want := range map[string]string{vague.ID: "vague", noEv.ID: "no evidence", taskCode.ID: "no MonoFlow command"} {
		if o := outcome(outs, id); o.Action != "rejected" || !strings.Contains(o.Reason, want) {
			t.Errorf("%s: want rejected (%s), got %+v", id, want, o)
		}
	}
	body := tr.issues[0].Body
	for _, leak := range []string{"alice", "abcdef1234567890"} {
		if strings.Contains(body, leak) {
			t.Fatalf("issue body leaks %q:\n%s", leak, body)
		}
	}
	if !strings.Contains(body, issueMarker) || !strings.Contains(tr.issues[0].Title, "improve(verify): ") || !strings.Contains(body, "abc1234") {
		t.Fatalf("issue: %q\n%s", tr.issues[0].Title, body)
	}
	if d := s.Decisions()[noEv.ID]; d.Status != "invalid" || !strings.HasPrefix(d.Note, autoNote) {
		t.Fatalf("rejected suggestions are marked invalid by auto-triage: %+v", d)
	}
	if again, _ := s.Publish(PublishOptions{Tracker: tr}); len(again) != 0 {
		t.Fatalf("handled suggestions are not triaged twice: %+v", again)
	}

	// Another session, same problem: a comment on the same issue, not a second issue.
	dup := add(t, s, Suggestion{Title: "verify should list the pending dependents", Area: "verify", Worktree: "w2", Evidence: "monoflow verify -> PENDING"})
	// The same problem as a rejected one, now with evidence: auto-rejection does not block it.
	better := add(t, s, Suggestion{Title: "ship should print the PR url", Area: "ship", Worktree: "w2", Evidence: "monoflow ship -m x -> status: shipping (no url)"})
	outs, _ = s.Publish(PublishOptions{Tracker: tr})
	if o := outcome(outs, dup.ID); o.Action != "commented" || o.Issue != 100 || len(tr.comments[100]) != 1 {
		t.Fatalf("duplicate must comment on #100: %+v %v", o, tr.comments)
	}
	if o := outcome(outs, better.ID); o.Action != "created" {
		t.Fatalf("a better-filed report of an auto-rejected problem is filed: %+v", o)
	}
	if tr.created != 2 {
		t.Fatalf("want 2 issues, got %d", tr.created)
	}
}

func TestPublishMaintainerVerdictsAndRetries(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	old := add(t, s, Suggestion{Title: "verify should autofix vet findings", Area: "verify", Worktree: "w1", Evidence: "monoflow verify -> vet"})
	if _, err := s.Mark([]string{old.ID}, "wontfix", "fixing code is the agent's job", true); err != nil {
		t.Fatal(err)
	}
	again := add(t, s, Suggestion{Title: "verify should autofix vet findings please", Area: "verify", Worktree: "w2", Evidence: "monoflow verify -> vet again"})
	tr := &fakeTracker{fail: fmt.Errorf("gh: not logged in")}
	outs, _ := s.Publish(PublishOptions{Tracker: tr})
	if o := outcome(outs, again.ID); o.Action != "rejected" || !strings.Contains(o.Reason, "wontfix") {
		t.Fatalf("a maintainer's wontfix declines the same problem: %+v", o)
	}
	if o := outcome(outs, old.ID); o.Action != "" {
		t.Fatalf("a suggestion the maintainer decided is not re-triaged: %+v", o)
	}

	fresh := add(t, s, Suggestion{Title: "events unread should hint at --all", Area: "events", Worktree: "w3", Evidence: "monoflow events unread -> other_branches_count: 3"})
	outs, _ = s.Publish(PublishOptions{Tracker: tr})
	if o := outcome(outs, fresh.ID); o.Action != "pending" || s.Publications()[fresh.ID].Attempts != 1 {
		t.Fatalf("tracker failure queues the suggestion: %+v", o)
	}
	tr.fail = nil
	tr.issues = []Issue{{Number: 7, Title: "improve(events): events unread should hint at --all", Body: issueMarker, URL: "u7"}}
	outs, _ = s.Publish(PublishOptions{Tracker: tr})
	if o := outcome(outs, fresh.ID); o.Action != "commented" || o.Issue != 7 {
		t.Fatalf("retry finds the issue another user filed and comments there: %+v", o)
	}
}

func TestRedactor(t *testing.T) {
	r := Redactor("/tmp/acme-secret")
	in := "cd /tmp/acme-secret/libs && GH_TOKEN=ghp_abcdefghijklmnopqrstuvwx1234 git push https://bob:hunter22@example.com/x Authorization: Bearer abcdefghijklmnopqrstuvwxyz mail bob@corp.com"
	out := r(in)
	for _, leak := range []string{"acme-secret", "ghp_", "hunter22", "abcdefghijklmnopqrstuvwxyz", "bob@corp.com"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "<repo>/libs") {
		t.Errorf("repo path becomes <repo>: %s", out)
	}
}
