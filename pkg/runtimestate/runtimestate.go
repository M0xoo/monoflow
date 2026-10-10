// Package runtimestate lists MonoFlow's local runtime files, which must never be committed.
package runtimestate

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Paths are repo-relative MonoFlow runtime state locations. Worktrees symlink some of them
// to the root repo, so ignore patterns must not use a trailing slash (git treats a symlink
// as a file, which "dir/" patterns do not match).
var Paths = []string{
	".agents/cache", ".agents/prs", ".agents/proofs", ".agents/events", ".agents/dist",
	".agents/subset.json", ".agents/circuit_breaker.json", ".agents/checkpoints",
	".agents/checkpoints.json", ".agents/outer_loop", ".agents/queue", ".agents/worktrees",
	".agents/traces", ".agents/hooks", ".agents/releases", GoWorkMarker,
}

// GoWorkMarker records the hash of a sparse worktree's projected go.work (see subset.SyncGoWork).
const GoWorkMarker = ".agents/gowork.projection"

// AddArgs returns `git add` arguments staging every change; follow it with UnstageArgs.
// (An ":(exclude)" pathspec is not used because git rejects pathspecs naming ignored files.)
func AddArgs() []string {
	return []string{"add", "-A"}
}

// UnstageArgs returns `git reset` arguments that drop runtime state staged earlier,
// including a sparse worktree's projected go.work.
func UnstageArgs(repoDir string) []string {
	args := append([]string{"reset", "-q", "--"}, Paths...)
	if ProjectedGoWork(repoDir) {
		args = append(args, "go.work")
	}
	return args
}

// ProjectedGoWork reports whether repoDir's go.work is the unmodified sparse projection
// written by MonoFlow (and therefore must never be committed or treated as a change).
func ProjectedGoWork(repoDir string) bool {
	want, err := os.ReadFile(filepath.Join(repoDir, GoWorkMarker))
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(repoDir, "go.work"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(want)) == HashBytes(data)
}

// HashBytes is the content hash used for projection markers.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// IgnorePatterns returns root-anchored ignore patterns for .gitignore / info/exclude.
// The locally installed binary is platform-specific and is ignored too (CI installs
// its own; see init --setup-gh-actions).
func IgnorePatterns() []string {
	out := make([]string, 0, len(Paths)+1)
	for _, p := range Paths {
		out = append(out, "/"+p)
	}
	return append(out, "/.agents/bin/monoflow")
}
