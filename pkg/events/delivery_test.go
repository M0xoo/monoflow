package events

import (
	"testing"
)

func TestDeliveryOncePerConsumer(t *testing.T) {
	em := NewEventManager(t.TempDir())
	_ = em.Emit(&Event{Type: EventCIFailed, CommitSHA: "aaa", Branch: "agent/a", RequiresAction: true, Summary: "red"})
	_ = em.Emit(&Event{Type: EventCIPassed, CommitSHA: "bbb", Branch: "agent/b", Summary: "green"})
	_ = em.Emit(&Event{Type: EventDependencyReleased, Summary: "money 2.0", RequiresAction: true,
		Payload: map[string]any{"consumer": "ledger", "mode": "pinned"}})
	_ = em.Emit(&Event{Type: EventDependencyReleased, Summary: "money 2.0 (source)",
		Payload: map[string]any{"consumer": "billing", "mode": "source"}})

	a := em.Pending("agent/a", "agent/a", map[string]bool{"ledger": true})
	if len(a) != 2 {
		t.Fatalf("agent/a should get its CI event and ledger's breaking release, got %d", len(a))
	}
	if err := em.MarkDelivered("agent/a", a); err != nil {
		t.Fatal(err)
	}
	if again := em.Pending("agent/a", "agent/a", map[string]bool{"ledger": true}); len(again) != 0 {
		t.Fatalf("events must be delivered once, got %d again", len(again))
	}
	b := em.Pending("agent/b", "agent/b", map[string]bool{"billing": true})
	if len(b) != 1 || b[0].Branch != "agent/b" {
		t.Fatalf("agent/b gets only its own CI event (source-consumer notes are informational), got %+v", b)
	}
}
