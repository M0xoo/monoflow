package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Delivery hands each consumer (an agent branch) every event relevant to it exactly once,
// tracked by a per-consumer cursor so agents never consume each other's events. Harness
// hooks call it to push events into the agent's context without the agent running a command.

var cursorName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (em *EventManager) cursorPath(consumer string) string {
	return filepath.Join(em.eventsDir, "cursors", cursorName.ReplaceAllString(consumer, "_")+".json")
}

func (em *EventManager) delivered(consumer string) map[string]bool {
	set := map[string]bool{}
	data, err := os.ReadFile(em.cursorPath(consumer))
	if err == nil {
		var ids []string
		_ = json.Unmarshal(data, &ids)
		for _, id := range ids {
			set[id] = true
		}
	}
	return set
}

// Relevant reports whether an event concerns the agent on branch whose worktree projects
// localPackages (nil = full checkout).
func Relevant(e *Event, branch string, localPackages map[string]bool) bool {
	if e.Branch != "" {
		return e.Branch == branch
	}
	if e.Type == EventDependencyReleased {
		consumer, _ := e.Payload["consumer"].(string)
		mode, _ := e.Payload["mode"].(string)
		if mode == "source" {
			return false // informational: the consumer already builds against live code
		}
		return localPackages == nil && e.RequiresAction || localPackages[consumer]
	}
	return true
}

// Pending returns relevant events not yet delivered to consumer, oldest first.
func (em *EventManager) Pending(consumer, branch string, localPackages map[string]bool) []*Event {
	all, err := em.List(false)
	if err != nil {
		return nil
	}
	done := em.delivered(consumer)
	var out []*Event
	for _, e := range all {
		// Read: acknowledged, or superseded by a newer commit's result (an agent handed a
		// replaced CI failure set out to fix what the newer push had already fixed).
		if !done[e.ID] && !e.Read && Relevant(e, branch, localPackages) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// MarkDelivered advances consumer's cursor past the given events.
func (em *EventManager) MarkDelivered(consumer string, evts []*Event) error {
	em.mu.Lock()
	defer em.mu.Unlock()
	set := em.delivered(consumer)
	for _, e := range evts {
		set[e.ID] = true
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	_ = os.MkdirAll(filepath.Dir(em.cursorPath(consumer)), 0755)
	data, _ := json.Marshal(ids)
	tmp := em.cursorPath(consumer) + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, em.cursorPath(consumer))
}

// Render formats events compactly for an agent's context.
func Render(evts []*Event) string {
	var b strings.Builder
	b.WriteString("[MonoFlow events]\n")
	for _, e := range evts {
		fmt.Fprintf(&b, "- %s: %s", e.Type, e.Summary)
		if e.RequiresAction && e.SuggestedTask != "" {
			fmt.Fprintf(&b, "\n  → %s", e.SuggestedTask)
		}
		if logs, ok := e.Payload["failure_logs"].(string); ok && logs != "" {
			if len(logs) > 800 {
				logs = "…" + logs[len(logs)-800:]
			}
			fmt.Fprintf(&b, "\n  failure: %s", strings.ReplaceAll(logs, "\n", "\n    "))
		}
		if s, ok := e.Payload["error_summary"].(string); ok && s != "" {
			if len(s) > 800 {
				s = s[:800] + " …"
			}
			fmt.Fprintf(&b, "\n  failures: %s", strings.ReplaceAll(s, "\n", "\n    "))
		}
		b.WriteString("\n")
	}
	return b.String()
}
