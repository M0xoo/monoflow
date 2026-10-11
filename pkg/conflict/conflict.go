// Package conflict keeps conflicts with the base branch from reaching the remote, and tells the
// agent that pushed a branch when the base later moves into a conflict with it.
//
// Every push path (push, the queued push after local CI, pr create / ship) calls Check first and
// refuses a branch that does not merge cleanly into origin/<base>. After a push, Watch runs in the
// background and re-checks the pushed branch against the base as the base moves, until the branch
// is merged or deleted, emitting PR_CONFLICT for the agent. Before this, only the remote CI watcher
// looked (and only while CI ran), and PRs reached GitHub conflicting with main with nobody told.
//
// The merge is done in memory with `git merge-tree --write-tree` (git 2.38+): nothing in any
// checkout changes.
package conflict

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/antigravity/monoflow/pkg/events"
)

// Result of merging a commit into the base.
type Result struct {
	Base    string   // base branch name ("main")
	Files   []string // files that conflict; empty when it merges cleanly
	Checked bool     // false: nothing to compare (no remote base, old git, unknown commit)
}

// Conflicting reports whether the check found conflicts.
func (r Result) Conflicting() bool { return r.Checked && len(r.Files) > 0 }

// BaseBranch is origin's main or master ("" when origin has neither).
func BaseBranch(root string) string {
	for _, b := range []string{"main", "master"} {
		if git(root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b) == nil {
			return b
		}
	}
	return ""
}

// Check fetches origin/<base> (bounded) and merges rev (a commit, a branch, "HEAD") into it in
// memory. rev being the base itself (or on it) is never a conflict.
func Check(root, rev string) Result {
	base := BaseBranch(root)
	if base == "" {
		return Result{}
	}
	fetch(root, base)
	return mergeTree(root, base, "origin/"+base, rev)
}

func mergeTree(root, base, baseRef, rev string) Result {
	out, err := exec.Command("git", "-C", root, "merge-tree", "--write-tree", "--name-only", "--no-messages", baseRef, rev).Output()
	if err == nil {
		return Result{Base: base, Checked: true}
	}
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		return Result{Base: base} // merge-tree unavailable or failed: unknown
	}
	res := Result{Base: base, Checked: true}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, l := range lines[min(1, len(lines)):] { // first line: the tree id
		if l = strings.TrimSpace(l); l == "" {
			break
		}
		res.Files = append(res.Files, l)
	}
	return res
}

// Next tells the agent how to resolve, in MonoFlow's commands.
func Next(base string, files []string) string {
	return fmt.Sprintf("monoflow sync --merge (brings origin/%s in), resolve the conflict markers in %s, monoflow verify, then monoflow ship -m \"<msg>\"", base, strings.Join(files, ", "))
}

// Refusal is the error a push path returns for a conflicting branch.
func Refusal(r Result) error {
	return fmt.Errorf("not pushed: the branch conflicts with origin/%s in %s. Next: %s", r.Base, strings.Join(r.Files, ", "), Next(r.Base, r.Files))
}

// Emit records PR_CONFLICT for branch, once per state of the branch (head) and the base.
func Emit(root, branch, head string, r Result) {
	baseSHA := revParse(root, "origin/"+r.Base)
	id := "evt_pr_conflict_" + short(head) + "_" + short(baseSHA)
	em := events.NewEventManager(root)
	if _, err := em.Get(id); err == nil {
		return // already reported for these commits
	}
	_ = em.Emit(&events.Event{
		ID:             id,
		Type:           events.EventPRConflict,
		CommitSHA:      head,
		Branch:         branch,
		RequiresAction: true,
		Summary:        fmt.Sprintf("%s conflicts with origin/%s in %s", branch, r.Base, strings.Join(r.Files, ", ")),
		SuggestedTask:  Next(r.Base, r.Files),
		Payload:        map[string]any{"base": "origin/" + r.Base, "files": r.Files},
	})
}

// GuardPush is what every push path runs first: Check the commit about to be pushed, and on a
// conflict emit PR_CONFLICT for the branch and return the refusal.
func GuardPush(root, branch, rev string) error {
	r := Check(root, rev)
	if !r.Conflicting() {
		return nil
	}
	Emit(root, branch, revParse(root, rev), r)
	return Refusal(r)
}

// --- the watcher after a push ---

// WatchInterval is how often a pushed branch is re-checked (MONOFLOW_CONFLICT_INTERVAL seconds).
func WatchInterval() time.Duration {
	if s, err := strconv.Atoi(os.Getenv("MONOFLOW_CONFLICT_INTERVAL")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 2 * time.Minute
}

const watchMaxAge = 48 * time.Hour

func watchMarker(root, branch string) string {
	return filepath.Join(root, ".agents", "conflict", strings.ReplaceAll(branch, "/", "_")+".pid")
}

// StartWatch launches the background watcher for a pushed branch, unless one runs already.
func StartWatch(root, branch string) {
	if branch == "" || os.Getenv("MONOFLOW_NO_CONFLICT_WATCH") != "" {
		return
	}
	if pidAlive(watchMarker(root, branch)) {
		return
	}
	exe, err := os.Executable()
	if err != nil || strings.HasSuffix(filepath.Base(exe), ".test") {
		return // never re-execute a Go test binary
	}
	cmd := exec.Command(exe, "conflict-watch", "--branch="+branch)
	cmd.Dir = root
	cmd.SysProcAttr = detached()
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// Watch re-checks origin/<branch> against origin/<base> every WatchInterval, emitting
// PR_CONFLICT when the base moves into a conflict with it, until the branch is merged into the
// base, deleted on origin, or watchMaxAge passes. One watcher per branch.
func Watch(root, branch string) {
	marker := watchMarker(root, branch)
	_ = os.MkdirAll(filepath.Dir(marker), 0755)
	me := strconv.Itoa(os.Getpid())
	_ = os.WriteFile(marker, []byte(me), 0644)
	defer func() {
		if data, _ := os.ReadFile(marker); string(data) == me {
			_ = os.Remove(marker)
		}
	}()
	start := time.Now()
	for time.Since(start) < watchMaxAge {
		if data, _ := os.ReadFile(marker); string(data) != me {
			return // a newer watcher took over
		}
		base := BaseBranch(root)
		if base == "" {
			return
		}
		fetch(root, base)
		fetch(root, branch)
		remoteBranch := "origin/" + branch
		head := revParse(root, remoteBranch)
		if head == "" {
			return // deleted on origin
		}
		if git(root, "merge-base", "--is-ancestor", remoteBranch, "origin/"+base) == nil {
			return // merged
		}
		if r := mergeTree(root, base, "origin/"+base, remoteBranch); r.Conflicting() {
			Emit(root, branch, head, r)
		}
		time.Sleep(WatchInterval())
	}
}

// --- helpers ---

func fetch(root, ref string) {
	cmd := exec.Command("git", "-C", root, "fetch", "--quiet", "origin", ref)
	if cmd.Start() != nil {
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func git(root string, args ...string) error {
	return exec.Command("git", append([]string{"-C", root}, args...)...).Run()
}

func revParse(root, rev string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", rev+"^{commit}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func short(sha string) string { return sha[:min(10, len(sha))] }

func pidAlive(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
