package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Speculative verification: verify runs while the agent is still thinking, not after it asks.
// After each tool call the hooks call maybeVerifyInBackground: if the uncommitted state of the
// tree changed, a detached `monoflow verify` starts for that exact state (after a short pause,
// so a burst of edits collapses into one run; a newer state cancels an older run). Its output
// is stored under the state's key. The agent's own `monoflow verify` then prints the stored
// result, or waits for the run already in progress, instead of starting from scratch.
// Failures are not pushed to the agent: mid-edit states are expected to fail.

// Triggers: the post-tool-call hook (Claude Code PostToolUse, opencode tool.execute.after) and,
// for harnesses without hooks (or subagents, which do not run them), a watcher that `start`
// launches in the worktree: it polls the tree state every second and exits after an hour
// without change or when the worktree is gone.

const speculateDebounce = 1500 * time.Millisecond

func specDir(root string) string { return filepath.Join(root, ".agents", "verify") }

// treeStateKey identifies the uncommitted state of root: HEAD, the diff against it and the
// new (untracked, not ignored) files. "" when there is nothing uncommitted or no git.
func treeStateKey(root string) string {
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	diff, _ := exec.Command("git", "-C", root, "diff", "HEAD", "--binary", "--", ".", ":(exclude).agents").Output()
	untracked, _ := exec.Command("git", "-C", root, "ls-files", "--others", "--exclude-standard", "-z", "--", ".", ":(exclude).agents").Output()
	if len(diff) == 0 && len(untracked) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write(head)
	h.Write(diff)
	for _, f := range strings.Split(strings.TrimRight(string(untracked), "\x00"), "\x00") {
		if f == "" {
			continue
		}
		h.Write([]byte(f))
		if data, err := os.ReadFile(filepath.Join(root, f)); err == nil && len(data) < 4<<20 {
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// maybeVerifyInBackground starts a background verify for the current state unless one ran or
// runs for it already. Cheap when nothing changed (one git diff).
func maybeVerifyInBackground(root string) {
	if os.Getenv("MONOFLOW_RECORD_KEY") != "" || os.Getenv("MONOFLOW_NO_SPECULATE") != "" {
		return
	}
	// Only in agent worktrees: the shared root may hold someone's half-done work, and nobody
	// asked for its CPU.
	if !strings.Contains(filepath.ToSlash(root), "/.agents/worktrees/") {
		return
	}
	key := treeStateKey(root)
	if key == "" {
		return
	}
	dir := specDir(root)
	for _, ext := range []string{".out", ".pid"} {
		if _, err := os.Stat(filepath.Join(dir, key+ext)); err == nil {
			return // done or running for this state
		}
	}
	_ = os.MkdirAll(dir, 0755)
	stopOtherSpecRuns(root, "") // a run for an older state is obsolete
	exe, err := os.Executable()
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, key+".tmp")
	script := fmt.Sprintf("sleep %.1f; MONOFLOW_RECORD_KEY=%s %q verify > %q 2>&1; rm -f %q",
		speculateDebounce.Seconds(), key, exe, tmp, filepath.Join(dir, key+".pid"))
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = root
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, key+".pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0644)
	_ = cmd.Process.Release()
	pruneSpecResults(dir, 20)
}

// speculatedResult returns the stored verify output for the current state, waiting (up to
// timeout) for a background run in progress. ok=false: run verify normally.
func speculatedResult(root string, timeout time.Duration) (string, bool) {
	key := treeStateKey(root)
	if key == "" {
		return "", false
	}
	dir := specDir(root)
	pid := filepath.Join(dir, key+".pid")
	deadline := time.Now().Add(timeout)
	for {
		_, running := os.Stat(pid)
		if running != nil {
			break
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(300 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(dir, key+".out"))
	if err != nil || len(data) == 0 || treeStateKey(root) != key {
		return "", false
	}
	return string(data), true
}

// finishRecord, in a background run, files its output (written to <key>.tmp) under the state
// key, but only if the tree is still in that state: a result for files that changed mid-run
// would be wrong.
func finishRecord(root string) {
	key := os.Getenv("MONOFLOW_RECORD_KEY")
	if key == "" {
		return
	}
	tmp := filepath.Join(specDir(root), key+".tmp")
	if treeStateKey(root) == key {
		_ = os.Rename(tmp, filepath.Join(specDir(root), key+".out"))
	} else {
		_ = os.Remove(tmp)
	}
}

// stopOtherSpecRuns stops background runs for states other than keep ("" stops all).
func stopOtherSpecRuns(root, keep string) {
	runs, _ := filepath.Glob(filepath.Join(specDir(root), "*.pid"))
	for _, r := range runs {
		k := strings.TrimSuffix(filepath.Base(r), ".pid")
		if k == keep {
			continue
		}
		if data, err := os.ReadFile(r); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				killGroup(pid)
			}
		}
		_ = os.Remove(r)
		_ = os.Remove(filepath.Join(specDir(root), k+".tmp"))
	}
}

func pruneSpecResults(dir string, keep int) {
	outs, _ := filepath.Glob(filepath.Join(dir, "*.out"))
	if len(outs) <= keep {
		return
	}
	type f struct {
		p string
		t time.Time
	}
	var fs []f
	for _, o := range outs {
		if fi, err := os.Stat(o); err == nil {
			fs = append(fs, f{o, fi.ModTime()})
		}
	}
	for len(fs) > keep {
		oldest := 0
		for i := range fs {
			if fs[i].t.Before(fs[oldest].t) {
				oldest = i
			}
		}
		_ = os.Remove(fs[oldest].p)
		fs = append(fs[:oldest], fs[oldest+1:]...)
	}
}

// startSpeculateWatcher launches the detached watcher for a new worktree.
func startSpeculateWatcher(wt string) {
	if os.Getenv("MONOFLOW_NO_SPECULATE") != "" {
		return
	}
	if _, err := os.Stat(wt); err != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "speculate-watch")
	cmd.Dir = wt
	cmd.SysProcAttr = detachedSysProcAttr()
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// speculateWatch polls the worktree's state and starts background verifies as it changes.
func speculateWatch(root string) {
	if !strings.Contains(filepath.ToSlash(root), "/.agents/worktrees/") {
		return
	}
	_ = os.MkdirAll(specDir(root), 0755)
	me := strconv.Itoa(os.Getpid())
	marker := filepath.Join(specDir(root), "watch.pid")
	_ = os.WriteFile(marker, []byte(me), 0644)
	last, lastChange := "", time.Now()
	for time.Since(lastChange) < time.Hour {
		time.Sleep(time.Second)
		if data, err := os.ReadFile(marker); err != nil || string(data) != me {
			return // worktree removed, or a newer watcher took over
		}
		if k := treeStateKey(root); k != last {
			last, lastChange = k, time.Now()
			maybeVerifyInBackground(root)
		}
	}
	_ = os.Remove(marker)
}
