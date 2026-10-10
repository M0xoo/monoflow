package events

import (
	"os"
	"testing"
	"time"
)

func TestEventEmissionAndQuery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow-events-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	em := NewEventManager(tmpDir)

	// Emit a pass event
	evt1 := &Event{
		Type:           EventCIPassed,
		CommitSHA:      "80ffc153f1208154",
		Branch:         "main",
		RequiresAction: false,
		Summary:        "All workflows passed",
	}
	if err := em.Emit(evt1); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	// Emit a failure event
	evt2 := &Event{
		Type:           EventCIFailed,
		CommitSHA:      "9fa84b1e7c902d33",
		Branch:         "feat/api",
		RequiresAction: true,
		Summary:        "Test failed in pkg/api",
		SuggestedTask:  "Fix test assertions",
	}
	if err := em.Emit(evt2); err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	// List all events
	all, err := em.List(false)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 events, got %d", len(all))
	}

	// List unread events
	unread, err := em.List(true)
	if err != nil {
		t.Fatalf("List unread failed: %v", err)
	}
	if len(unread) != 2 {
		t.Fatalf("expected 2 unread events, got %d", len(unread))
	}

	// Ack one event
	if err := em.Ack(evt1.ID); err != nil {
		t.Fatalf("Ack failed: %v", err)
	}

	unreadAfterAck, err := em.List(true)
	if err != nil {
		t.Fatalf("List unread after ack failed: %v", err)
	}
	if len(unreadAfterAck) != 1 {
		t.Fatalf("expected 1 unread event, got %d", len(unreadAfterAck))
	}
	if unreadAfterAck[0].ID != evt2.ID {
		t.Errorf("expected remaining unread event to be %s, got %s", evt2.ID, unreadAfterAck[0].ID)
	}
}

func TestCIWatcherAsyncStart(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "monoflow-watch-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cw := NewCIWatcher(tmpDir)
	spawned := 0
	cw.Spawn = func(*WatchJob, WatchOptions) error { spawned++; return nil }

	start := time.Now()
	job, err := cw.StartWatch(WatchOptions{
		CommitSHA:       "abcdef1234567890",
		Branch:          "main",
		PollIntervalSec: 1,
		TimeoutMin:      1,
		Async:           true, // Must return immediately!
	})
	dur := time.Since(start)

	if err != nil {
		t.Fatalf("StartWatch failed: %v", err)
	}

	// Verify non-blocking return in < 50ms
	if dur > 100*time.Millisecond {
		t.Errorf("StartWatch async took too long: %v (expected < 100ms)", dur)
	}

	if job.Status != WatchStatusMonitoring {
		t.Errorf("expected initial status 'monitoring', got '%s'", job.Status)
	}
	if spawned != 1 {
		t.Errorf("expected one detached supervisor, got %d", spawned)
	}
	// A second watch of the same commit reuses the active job instead of spawning again.
	again, _ := cw.StartWatch(WatchOptions{CommitSHA: "abcdef1234567890", Branch: "main", Async: true})
	if again == nil || again.ID != job.ID || spawned != 1 {
		t.Errorf("expected watcher reuse for the same commit, got %+v (spawned %d)", again, spawned)
	}

	// Verify job persisted
	loaded, err := cw.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if loaded.CommitSHA != "abcdef1234567890" {
		t.Errorf("CommitSHA mismatch: %s", loaded.CommitSHA)
	}
}
