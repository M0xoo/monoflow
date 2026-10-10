package events

import (
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
		evt.ID = fmt.Sprintf("evt_%s_%s_%d", strings.ToLower(string(evt.Type)), shortSHA, time.Now().UnixNano()/int64(time.Millisecond))
	}

	if evt.CreatedAt.IsZero() {
		evt.CreatedAt = time.Now().UTC()
	}

	path := filepath.Join(em.eventsDir, fmt.Sprintf("%s.json", evt.ID))
	data, err := json.MarshalIndent(evt, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize event: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	if (evt.Type == EventCIPassed || evt.Type == EventCIFailed) && evt.Branch != "" && evt.CommitSHA != "" {
		em.supersedeOlderCI(evt)
	}
	return nil
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
			(old.Type != EventCIPassed && old.Type != EventCIFailed) || old.CommitSHA == newer.CommitSHA {
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
