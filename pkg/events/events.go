package events

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EventType categorizes asynchronous system events for agents.
type EventType string

const (
	EventCIPassed           EventType = "CI_PASSED"
	EventCIFailed           EventType = "CI_FAILED"
	EventOuterRegression    EventType = "OUTER_LOOP_REGRESSION"
	EventSpeculativePass    EventType = "SPECULATIVE_MERGE_PASS"
	EventSpeculativeFail    EventType = "SPECULATIVE_MERGE_FAIL"
	EventCircuitTripped     EventType = "CIRCUIT_BREAKER_TRIPPED"
	EventPRCreated          EventType = "PR_CREATED"
	EventPRMerged           EventType = "PR_MERGED"
	EventPRReviewed         EventType = "PR_REVIEWED"
	EventPRTested           EventType = "PR_TESTED"
	EventPRConflict         EventType = "PR_CONFLICT"
	EventCISkipped          EventType = "CI_SKIPPED"
	EventOuterPassed        EventType = "OUTER_LOOP_PASSED"
	EventDependencyReleased EventType = "DEPENDENCY_RELEASED"
	// EventShipFailed: a background `monoflow ship` could not push / open the PR (red outer
	// loop, blocked PR gate, push error).
	EventShipFailed EventType = "SHIP_FAILED"
)

// Event represents a machine-readable asynchronous event dispatched to agents.
type Event struct {
	ID             string         `json:"id"`
	Type           EventType      `json:"type"`
	CommitSHA      string         `json:"commit_sha,omitempty"`
	Branch         string         `json:"branch,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	Read           bool           `json:"read"`
	RequiresAction bool           `json:"requires_action"`
	Summary        string         `json:"summary"`
	SuggestedTask  string         `json:"suggested_task,omitempty"`
	Payload        map[string]any `json:"payload,omitempty"`
}

// EventManager handles persisting, querying, and acknowledging agent events.
type EventManager struct {
	mu        sync.Mutex
	RootDir   string
	eventsDir string
}

// NewEventManager creates a new event manager.
func NewEventManager(rootDir string) *EventManager {
	dir := filepath.Join(rootDir, ".agents", "events")
	_ = os.MkdirAll(dir, 0755)
	return &EventManager{
		RootDir:   rootDir,
		eventsDir: dir,
	}
}

// Emit writes a new event to the event bus.
func (em *EventManager) Emit(evt *Event) error {
	em.mu.Lock()
	defer em.mu.Unlock()

	if evt.ID == "" {
		shortSHA := evt.CommitSHA
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}
		// Nanoseconds + random suffix: several events of one type (e.g. a release notifying
		// every consumer) are emitted within the same millisecond and must not overwrite.
		var rnd [4]byte
		_, _ = rand.Read(rnd[:])
		evt.ID = fmt.Sprintf("evt_%s_%s_%d_%x", strings.ToLower(string(evt.Type)), shortSHA, time.Now().UnixNano(), rnd)
	}

	if evt.CreatedAt.IsZero() {
		evt.CreatedAt = time.Now().UTC()
	}

	path := filepath.Join(em.eventsDir, fmt.Sprintf("%s.json", evt.ID))
	data, err := json.MarshalIndent(evt, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize event: %w", err)
	}

	if err := writeAtomic(path, data); err != nil {
		return err
	}
	if supersedable(evt.Type) && evt.Branch != "" && evt.CommitSHA != "" {
		em.supersedeOlderCI(evt)
	}
	return nil
}

// supersedable results describe one commit of a branch; a newer commit's result replaces them.
func supersedable(t EventType) bool {
	return t == EventCIPassed || t == EventCIFailed || t == EventCISkipped || t == EventOuterPassed || t == EventOuterRegression
}

// sameFamily: remote CI results supersede remote CI results, outer-loop results outer-loop ones.
func sameFamily(a, b EventType) bool {
	outer := func(t EventType) bool { return t == EventOuterPassed || t == EventOuterRegression }
	return supersedable(a) && supersedable(b) && outer(a) == outer(b)
}

// supersedeOlderCI marks unread CI results for older commits of the same branch as read,
// so agents never act on a failure that a newer push already replaced.
func (em *EventManager) supersedeOlderCI(newer *Event) {
	entries, err := os.ReadDir(em.eventsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == newer.ID+".json" {
			continue
		}
		path := filepath.Join(em.eventsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var old Event
		if json.Unmarshal(data, &old) != nil || old.Read || old.Branch != newer.Branch ||
			!sameFamily(old.Type, newer.Type) || old.CommitSHA == newer.CommitSHA {
			continue
		}
		anc := exec.Command("git", "merge-base", "--is-ancestor", old.CommitSHA, newer.CommitSHA)
		anc.Dir = em.RootDir
		if anc.Run() != nil {
			continue
		}
		old.Read = true
		if old.Payload == nil {
			old.Payload = map[string]any{}
		}
		old.Payload["superseded_by"] = newer.ID
		if out, err := json.MarshalIndent(old, "", "  "); err == nil {
			_ = os.WriteFile(path, out, 0644)
		}
	}
}

// AckSkipped marks unread CI_SKIPPED results for sha as read: a new watch (e.g. the PR was just
// opened) means a real result is coming, and the stale "skipped" must not answer for it.
func (em *EventManager) AckSkipped(sha string) {
	list, _ := em.List(true)
	for _, e := range list {
		if e.Type == EventCISkipped && e.CommitSHA != "" && sha != "" &&
			(strings.HasPrefix(e.CommitSHA, sha) || strings.HasPrefix(sha, e.CommitSHA)) {
			_ = em.Ack(e.ID)
		}
	}
}

// List returns all recorded events, optionally filtered to unread only.
func (em *EventManager) List(unreadOnly bool) ([]*Event, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	entries, err := os.ReadDir(em.eventsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Event{}, nil
		}
		return nil, err
	}

	var events []*Event
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(em.eventsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var evt Event
		if err := json.Unmarshal(data, &evt); err == nil {
			if !unreadOnly || !evt.Read {
				events = append(events, &evt)
			}
		}
	}

	// Sort newest first
	sort.Slice(events, func(i, j int) bool {
		return events[i].CreatedAt.After(events[j].CreatedAt)
	})

	return events, nil
}

// Get retrieves a specific event by ID.
func (em *EventManager) Get(eventID string) (*Event, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	path := filepath.Join(em.eventsDir, fmt.Sprintf("%s.json", eventID))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("event '%s' not found: %w", eventID, err)
	}

	var evt Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil, fmt.Errorf("corrupted event file: %w", err)
	}
	return &evt, nil
}

// Ack marks an event as read/handled.
func (em *EventManager) Ack(eventID string) error {
	em.mu.Lock()
	defer em.mu.Unlock()

	path := filepath.Join(em.eventsDir, fmt.Sprintf("%s.json", eventID))
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("event '%s' not found: %w", eventID, err)
	}

	var evt Event
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("corrupted event file: %w", err)
	}

	evt.Read = true
	updatedData, err := json.MarshalIndent(evt, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, updatedData, 0644)
}

// ClearAll removes all acknowledged events.
func (em *EventManager) ClearAll() error {
	em.mu.Lock()
	defer em.mu.Unlock()

	entries, err := os.ReadDir(em.eventsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			_ = os.Remove(filepath.Join(em.eventsDir, entry.Name()))
		}
	}
	return nil
}
