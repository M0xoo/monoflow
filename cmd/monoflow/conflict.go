package main

import (
	"os/exec"
	"strings"

	"github.com/antigravity/monoflow/pkg/conflict"
)

// Conflicts with the base branch (see pkg/conflict): ship and pr create refuse up front with the
// files and the way out; push paths refuse at the moment of pushing; a pushed branch is watched
// in the background, and also checked here when the agent ends its turn or reads its events.

// baseConflicts checks the current branch (HEAD) against origin/<base>.
func baseConflicts(root string) (base string, files []string, checked bool) {
	branch := currentBranch(root)
	if b := conflict.BaseBranch(root); b == "" || branch == "" || branch == b || branch == "HEAD" {
		return b, nil, false
	}
	r := conflict.Check(root, "HEAD")
	return r.Base, r.Files, r.Checked
}

func conflictNext(base string, files []string) string { return conflict.Next(base, files) }

// emitConflict records PR_CONFLICT for the current branch (once per branch head and base).
func emitConflict(root, base string, files []string) {
	head, _ := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	conflict.Emit(root, currentBranch(root), strings.TrimSpace(string(head)), conflict.Result{Base: base, Files: files, Checked: true})
}

// checkPushedBranchConflicts reports conflicts of an already-pushed branch with a base that
// moved since, when the agent ends its turn or reads its events (the background watcher does
// it between those).
func checkPushedBranchConflicts(root string) {
	branch := currentBranch(root)
	if branch == "" || exec.Command("git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch).Run() != nil {
		return // not pushed: the push paths check before pushing
	}
	if base, files, checked := baseConflicts(root); checked && len(files) > 0 {
		emitConflict(root, base, files)
	}
}
