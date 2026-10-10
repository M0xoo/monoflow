// Package runtimestate lists MonoFlow's local runtime files, which must never be committed.
package runtimestate

// Paths are repo-relative MonoFlow runtime state locations. Worktrees symlink some of them
// to the root repo, so ignore patterns must not use a trailing slash (git treats a symlink
// as a file, which "dir/" patterns do not match).
var Paths = []string{
	".agents/cache", ".agents/prs", ".agents/proofs", ".agents/events", ".agents/dist",
	".agents/subset.json", ".agents/circuit_breaker.json", ".agents/checkpoints",
	".agents/checkpoints.json", ".agents/outer_loop", ".agents/queue", ".agents/worktrees",
	".agents/traces", ".agents/hooks",
}

// AddArgs returns `git add` arguments staging every change; follow it with UnstageArgs.
// (An ":(exclude)" pathspec is not used because git rejects pathspecs naming ignored files.)
func AddArgs() []string {
	return []string{"add", "-A"}
}

// UnstageArgs returns `git reset` arguments that drop runtime state staged earlier.
func UnstageArgs() []string {
	return append([]string{"reset", "-q", "--"}, Paths...)
}

// IgnorePatterns returns root-anchored ignore patterns for .gitignore / info/exclude.
func IgnorePatterns() []string {
	out := make([]string, len(Paths))
	for i, p := range Paths {
		out[i] = "/" + p
	}
	return out
}
