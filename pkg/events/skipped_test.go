package events

import (
	"os/exec"
	"strings"
	"testing"
)

// A CI_SKIPPED recorded before the PR existed must not answer for the PR's CI run on the same
// commit ('events wait' returned the stale skip right after 'pr create').
func TestAckSkippedClearsStaleSkipForCommit(t *testing.T) {
	em := NewEventManager(t.TempDir())
	_ = em.Emit(&Event{Type: EventCISkipped, CommitSHA: "abc1234def", Branch: "agent/x"})
	_ = em.Emit(&Event{Type: EventCISkipped, CommitSHA: "fff0000aaa", Branch: "agent/y"})
	em.AckSkipped("abc1234")
	list, _ := em.List(true)
	if len(list) != 1 || list[0].CommitSHA != "fff0000aaa" {
		t.Fatalf("unread after AckSkipped = %+v, want only the other commit's skip", list)
	}
}

// An outer-loop result for a newer commit of the branch retires the older one: agents only see
// the state of their current head (events unread listed every past OUTER_LOOP_PASSED).
func TestNewerOuterResultSupersedesOlder(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "one")
	first := run("rev-parse", "HEAD")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "two")
	second := run("rev-parse", "HEAD")
	em := NewEventManager(root)
	_ = em.Emit(&Event{Type: EventOuterPassed, CommitSHA: first, Branch: "agent/x"})
	_ = em.Emit(&Event{Type: EventCIFailed, CommitSHA: first, Branch: "agent/x"})
	_ = em.Emit(&Event{Type: EventOuterPassed, CommitSHA: second, Branch: "agent/x"})
	list, _ := em.List(true)
	if len(list) != 2 {
		t.Fatalf("unread = %d, want the new outer result and the (different family) CI failure", len(list))
	}
	for _, e := range list {
		if e.Type == EventOuterPassed && e.CommitSHA != second {
			t.Fatal("older outer-loop result still unread")
		}
	}
}
