// Package improve collects retrospectives on MonoFlow itself from the agents that use it.
//
// Every agent-run MonoFlow command is journaled (command, exit code, duration, worktree).
// When a session ends, the agent runs `monoflow improve`: it sees what it ran (failures,
// commands MonoFlow redirected, slow calls) and files what MonoFlow should do better with
// `improve add`, or `improve none`. Suggestions are plain JSONL files, so a maintainer (or a
// maintaining agent) can triage them across sessions: `improve triage` groups duplicates and
// ranks them by how many sessions reported them; `improve mark` records the decision.
//
// Storage: $MONOFLOW_IMPROVE_DIR, else <main repo root>/.agents/improve (shared by every
// worktree of the repo, never committed).
package improve

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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

// Entry is one journaled MonoFlow invocation (Kind "cmd") or a raw command MonoFlow
// redirected through a harness hook (Kind "redirect").
type Entry struct {
	TS       time.Time `json:"ts"`
	Kind     string    `json:"kind"`
	Cmd      string    `json:"cmd"`
	Exit     int       `json:"exit"`
	Ms       int64     `json:"ms,omitempty"`
	Worktree string    `json:"worktree"`
	Branch   string    `json:"branch,omitempty"`
	Agent    string    `json:"agent,omitempty"` // MONOFLOW_AGENT, when the harness names its agents
}

// Suggestion is one improvement an agent asks MonoFlow for.
type Suggestion struct {
	ID       string    `json:"id"`
	TS       time.Time `json:"ts"`
	Title    string    `json:"title"`
	Area     string    `json:"area,omitempty"`     // MonoFlow command or topic: verify, ship, docs...
	Kind     string    `json:"kind,omitempty"`     // bug | friction | missing | docs
	Severity string    `json:"severity,omitempty"` // high | medium | low
	Detail   string    `json:"detail,omitempty"`
	Evidence string    `json:"evidence,omitempty"` // exact command + output snippet
	Repo     string    `json:"repo,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Session  string    `json:"session,omitempty"`
}

// Retro closes a session: how many suggestions it filed (0 with None = nothing to improve).
type Retro struct {
	TS          time.Time `json:"ts"`
	Worktree    string    `json:"worktree"`
	Branch      string    `json:"branch,omitempty"`
	Session     string    `json:"session"`
	Agent       string    `json:"agent,omitempty"`
	Commands    int       `json:"commands"`
	Suggestions int       `json:"suggestions"`
	None        bool      `json:"none,omitempty"`
	Note        string    `json:"note,omitempty"`
}

// Decision is a triage verdict on a suggestion.
type Decision struct {
	Status string    `json:"status"` // accepted | fixed | wontfix | duplicate | invalid
	Note   string    `json:"note,omitempty"`
	TS     time.Time `json:"ts"`
}

// Statuses a suggestion can be marked with. Unmarked suggestions are "new".
var Statuses = []string{"accepted", "fixed", "wontfix", "duplicate", "invalid"}

// Open reports whether a status still needs work.
func Open(status string) bool { return status == "" || status == "new" || status == "accepted" }

// Store is the improvement data of one repository (or of MONOFLOW_IMPROVE_DIR).
type Store struct {
	Dir     string
	exclude string // git info/exclude that must list Dir (repos installed before it was gitignored)
}

var mu sync.Mutex

// OpenStore returns the store for the repository containing dir.
func OpenStore(dir string) *Store {
	if d := os.Getenv("MONOFLOW_IMPROVE_DIR"); d != "" {
		return &Store{Dir: d}
	}
	root := MainRoot(dir)
	s := &Store{Dir: filepath.Join(root, ".agents", "improve")}
	if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && fi.IsDir() {
		s.exclude = filepath.Join(root, ".git", "info", "exclude")
	}
	return s
}

// MainRoot resolves a linked worktree to the main repository root.
func MainRoot(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return dir
	}
	common := strings.TrimSpace(string(out))
	if filepath.Base(common) != ".git" {
		return dir
	}
	return filepath.Dir(common)
}

// mkdir creates the store directory, keeping it out of `git status` of the repository.
func (s *Store) mkdir() error {
	if _, err := os.Stat(s.Dir); err == nil {
		return nil
	}
	if err := os.MkdirAll(s.Dir, 0755); err != nil {
		return err
	}
	if s.exclude != "" {
		data, _ := os.ReadFile(s.exclude)
		if !strings.Contains(string(data), "/.agents/improve") {
			_ = os.MkdirAll(filepath.Dir(s.exclude), 0755)
			if f, err := os.OpenFile(s.exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
				_, _ = f.WriteString("\n# MonoFlow retros (monoflow improve)\n/.agents/improve\n")
				_ = f.Close()
			}
		}
	}
	return nil
}

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name) }

func (s *Store) appendJSON(name string, v any) error {
	mu.Lock()
	defer mu.Unlock()
	if err := s.mkdir(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

func readJSONL[T any](path string) []T {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var v T
		if json.Unmarshal(sc.Bytes(), &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// Record journals an invocation or redirect.
func (s *Store) Record(e Entry) error {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	if e.Agent == "" {
		e.Agent = os.Getenv("MONOFLOW_AGENT")
	}
	return s.appendJSON("journal.jsonl", e)
}

// Journal returns every journaled entry.
func (s *Store) Journal() []Entry { return readJSONL[Entry](s.path("journal.jsonl")) }

// Suggestions returns every filed suggestion, oldest first.
func (s *Store) Suggestions() []Suggestion { return readJSONL[Suggestion](s.path("suggestions.jsonl")) }

// Retros returns every closed retro.
func (s *Store) Retros() []Retro { return readJSONL[Retro](s.path("retros.jsonl")) }

// Session is the activity in one worktree since its last retro.
type Session struct {
	ID       string
	Worktree string
	Since    time.Time
	Entries  []Entry
	Filed    []Suggestion // suggestions already filed in this session
}

// Current returns the open session: journal entries after its last retro. A session is the
// agent named by MONOFLOW_AGENT when the harness sets it (concurrent agents share the main
// checkout before they `start`), else everything run in worktree.
func (s *Store) Current(worktree string) Session {
	agent := os.Getenv("MONOFLOW_AGENT")
	mine := func(wt, ag string) bool {
		if agent != "" {
			return ag == agent
		}
		return wt == worktree
	}
	var since time.Time
	for _, r := range s.Retros() {
		if mine(r.Worktree, r.Agent) && r.TS.After(since) {
			since = r.TS
		}
	}
	sess := Session{Worktree: worktree, Since: since}
	for _, e := range s.Journal() {
		if mine(e.Worktree, e.Agent) && e.TS.After(since) {
			sess.Entries = append(sess.Entries, e)
		}
	}
	for _, sg := range s.Suggestions() {
		if mine(sg.Worktree, sg.Agent) && sg.TS.After(since) {
			sess.Filed = append(sess.Filed, sg)
		}
	}
	start := since
	if len(sess.Entries) > 0 {
		start = sess.Entries[0].TS
	}
	key := worktree
	if agent != "" {
		key = "agent:" + agent
	}
	sum := sha256.Sum256([]byte(key + start.Format(time.RFC3339Nano)))
	sess.ID = hex.EncodeToString(sum[:4])
	return sess
}

// Commands counts the MonoFlow commands (not redirects) in the session.
func (sess Session) Commands() int {
	n := 0
	for _, e := range sess.Entries {
		if e.Kind == "cmd" {
			n++
		}
	}
	return n
}

// Worked reports whether the session did real MonoFlow work worth a retro.
func (sess Session) Worked() bool {
	for _, e := range sess.Entries {
		switch strings.Fields(e.Cmd + " x")[0] {
		case "start", "verify", "ship", "commit", "push", "pr", "worktree", "sync", "find", "graph":
			return true
		}
	}
	return false
}

// Add files a suggestion in the session of its worktree.
func (s *Store) Add(sg Suggestion) (Suggestion, error) {
	sg.Title = strings.TrimSpace(sg.Title)
	if sg.Title == "" {
		return sg, fmt.Errorf("a suggestion needs a title")
	}
	if sg.TS.IsZero() {
		sg.TS = time.Now().UTC()
	}
	if sg.Session == "" {
		sg.Session = s.Current(sg.Worktree).ID
	}
	sum := sha256.Sum256([]byte(sg.TS.Format(time.RFC3339Nano) + sg.Title + sg.Worktree))
	sg.ID = "s" + hex.EncodeToString(sum[:3])
	return sg, s.appendJSON("suggestions.jsonl", sg)
}

// Close ends the session of worktree.
func (s *Store) Close(worktree, branch, agent, note string, none bool) (Retro, error) {
	sess := s.Current(worktree)
	r := Retro{TS: time.Now().UTC(), Worktree: worktree, Branch: branch, Session: sess.ID, Agent: agent,
		Commands: sess.Commands(), Suggestions: len(sess.Filed), None: none && len(sess.Filed) == 0, Note: note}
	return r, s.appendJSON("retros.jsonl", r)
}

// Decisions returns the triage verdicts by suggestion id.
func (s *Store) Decisions() map[string]Decision {
	out := map[string]Decision{}
	if data, err := os.ReadFile(s.path("triage.json")); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// Mark records a verdict for each id and, unless exact, for every suggestion in the same
// cluster (duplicates of it). It returns the ids marked.
func (s *Store) Mark(ids []string, status, note string, exact bool) ([]string, error) {
	valid := false
	for _, st := range append([]string{"new"}, Statuses...) {
		valid = valid || st == status
	}
	if !valid {
		return nil, fmt.Errorf("unknown status %q (want one of new, %s)", status, strings.Join(Statuses, ", "))
	}
	all := s.Suggestions()
	known := map[string]bool{}
	for _, sg := range all {
		known[sg.ID] = true
	}
	target := map[string]bool{}
	for _, id := range ids {
		if !known[id] {
			return nil, fmt.Errorf("no suggestion %s (see monoflow improve list --all)", id)
		}
		target[id] = true
	}
	if !exact {
		for _, c := range Cluster(all) {
			hit := false
			for _, m := range c.Members {
				hit = hit || target[m.ID]
			}
			if hit {
				for _, m := range c.Members {
					target[m.ID] = true
				}
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	dec := s.Decisions()
	var marked []string
	for id := range target {
		if status == "new" {
			delete(dec, id)
		} else {
			dec[id] = Decision{Status: status, Note: note, TS: time.Now().UTC()}
		}
		marked = append(marked, id)
	}
	sort.Strings(marked)
	data, _ := json.MarshalIndent(dec, "", "  ")
	if err := s.mkdir(); err != nil {
		return nil, err
	}
	return marked, os.WriteFile(s.path("triage.json"), append(data, '\n'), 0644)
}

// ClaimNudge reports whether the agent of sess should be asked for a retro now: the session
// did MonoFlow work, filed no retro yet and was not asked before. Asking is recorded, so a
// session is asked once.
func (s *Store) ClaimNudge(sess Session) bool {
	if !sess.Worked() {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	path := s.path("nudged.json")
	seen := map[string]time.Time{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
	}
	if _, ok := seen[sess.ID]; ok {
		return false
	}
	seen[sess.ID] = time.Now().UTC()
	data, _ := json.Marshal(seen)
	_ = s.mkdir()
	return os.WriteFile(path, data, 0644) == nil
}
