package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/events"
)

// Conflicts with the base branch are found locally, where the agent is, instead of only by the
// remote CI watcher (which saw them only while CI ran, and only once GitHub had computed
// mergeability): an agent's PR conflicted with master and it never heard about it.
// `git merge-tree --write-tree` (git 2.38+) merges in memory: nothing in the checkout changes.

// baseConflicts fetches origin/<base> (bounded) and reports the files the current branch
// conflicts in when merged into it. checked=false when there is nothing to compare (on the
// base branch, no remote base, old git).
func baseConflicts(root string) (base string, files []string, checked bool) {
	branch := currentBranch(root)
	for _, b := range []string{"main", "master"} {
		if exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b).Run() == nil {
			base = b
			break
		}
	}
	if base == "" || branch == "" || branch == base || branch == "HEAD" {
		return base, nil, false
	}
	fetch := exec.Command("git", "-C", root, "fetch", "--quiet", "origin", base)
	if fetch.Start() == nil {
		done := make(chan error, 1)
		go func() { done <- fetch.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = fetch.Process.Kill()
			<-done
		}
	}
	out, err := exec.Command("git", "-C", root, "merge-tree", "--write-tree", "--name-only", "--no-messages", "origin/"+base, "HEAD").Output()
	if err == nil {
		return base, nil, true // merges cleanly
	}
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		return base, nil, false // merge-tree unavailable or failed: unknown
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, l := range lines[min(1, len(lines)):] { // first line: the tree id
		if l = strings.TrimSpace(l); l == "" {
			break
		}
		files = append(files, l)
	}
	return base, files, true
}

// conflictNext tells the agent how to resolve, in MonoFlow's commands.
func conflictNext(base string, files []string) string {
	return fmt.Sprintf("monoflow sync --merge (brings origin/%s in), resolve the conflict markers in %s, monoflow verify, then monoflow ship -m \"<msg>\"", base, strings.Join(files, ", "))
}

// emitConflict records a PR_CONFLICT event for the agent, once per state of the branch and
// the base.
func emitConflict(root, base string, files []string) {
	head, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	baseSHA, _ := exec.Command("git", "-C", root, "rev-parse", "origin/"+base).Output()
	h, b := strings.TrimSpace(string(head)), strings.TrimSpace(string(baseSHA))
	id := "evt_pr_conflict_" + h[:min(10, len(h))] + "_" + b[:min(10, len(b))]
	em := events.NewEventManager(root)
	if _, err := em.Get(id); err == nil {
		return // already reported for these commits
	}
	_ = em.Emit(&events.Event{
		ID:             id,
		Type:           events.EventPRConflict,
		CommitSHA:      h,
		Branch:         currentBranch(root),
		RequiresAction: true,
		Summary:        fmt.Sprintf("your branch conflicts with origin/%s in %s", base, strings.Join(files, ", ")),
		SuggestedTask:  conflictNext(base, files),
		Payload:        map[string]any{"base": "origin/" + base, "files": files},
	})
}

// checkPushedBranchConflicts reports (as an event) conflicts of an already-pushed branch with a
// base that moved since: run when the agent ends its turn or reads its events.
func checkPushedBranchConflicts(root string) {
	branch := currentBranch(root)
	if branch == "" || exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch).Run() != nil {
		return // not pushed: ship checks before pushing
	}
	if base, files, checked := baseConflicts(root); checked && len(files) > 0 {
		emitConflict(root, base, files)
	}
}
