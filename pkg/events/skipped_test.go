package events

import "testing"

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
