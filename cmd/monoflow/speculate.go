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

	"github.com/antigravity/monoflow/pkg/verify"
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
// It never cancels a background run for an older state: agents edit every few seconds, so
// cancelled runs never finished (trpc: an agent edited for a minute and its first verify still
// took 110 s). A finished run caches every package it checked, so packages the agent has not
// touched since are cache hits by the time it asks; the newest state starts once it is done.
// It reports whether the current state is handled (started, running or done).
func maybeVerifyInBackground(root string) bool {
	if os.Getenv("MONOFLOW_RECORD_KEY") != "" || os.Getenv("MONOFLOW_NO_SPECULATE") != "" {
		return true
	}
	// In agent worktrees, or in a checkout whose own verify started the watcher (the agent
	// asked to verify that tree). Never on a hook alone: a shared root may hold someone's
	// half-done work, and nobody asked for its CPU.
	if !speculationAllowed(root) {
		return true
	}
	key := treeStateKey(root)
	if key == "" {
		return true
	}
	dir := specDir(root)
	if _, err := os.Stat(filepath.Join(dir, key+".out")); err == nil || specRunAlive(dir, key) {
		return true // done or running for this state
	}
	if anySpecRunAlive(dir) {
		return false // let it finish; the watcher starts this state afterwards
	}
	_ = os.MkdirAll(dir, 0755)
	exe, err := os.Executable()
	if err != nil {
		return true
	}
	tmp := filepath.Join(dir, key+".tmp")
	// Always text: the stored result is printed to a plain verify. A watcher started under
	// MONOFLOW_FORMAT=json (local CI's environment) stored JSON, and plain verify printed it.
	script := fmt.Sprintf("sleep %.1f; MONOFLOW_FORMAT= MONOFLOW_RECORD_KEY=%s %q verify > %q 2>&1; rm -f %q",
		speculateDebounce.Seconds(), key, exe, tmp, filepath.Join(dir, key+".pid"))
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = root
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return true
	}
	_ = os.WriteFile(filepath.Join(dir, key+".pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0644)
	_ = cmd.Process.Release()
	pruneSpecResults(dir, 20)
	return true
}

// anySpecRunAlive reports a verify in progress for any state: a background run or the agent's own.
func anySpecRunAlive(dir string) bool {
	for _, ext := range []string{"*.pid", "*.run"} {
		marks, _ := filepath.Glob(filepath.Join(dir, ext))
		for _, m := range marks {
			if filepath.Base(m) == "watch.pid" {
				continue
			}
			if specRunAlive(dir, strings.TrimSuffix(strings.TrimSuffix(filepath.Base(m), ".pid"), ".run")) {
				return true
			}
		}
	}
	return false
}

// speculatedResult returns the stored verify output for the current state, waiting (up to
// timeout) for a background run in progress. ok=false: run verify normally.
func speculatedResult(root string, timeout time.Duration) (string, bool) {
	out, ok, _ := speculatedResultOrRunning(root, timeout)
	return out, ok
}

// speculatedResultOrRunning is speculatedResult that also reports a run for this state still
// going when timeout passed (the caller says so instead of starting the same checks again).
func speculatedResultOrRunning(root string, timeout time.Duration) (out string, ok, running bool) {
	key := treeStateKey(root)
	if key == "" {
		return "", false, false
	}
	dir := specDir(root)
	deadline := time.Now().Add(timeout)
	for specRunAlive(dir, key) {
		if time.Now().After(deadline) {
			return "", false, true
		}
		time.Sleep(300 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(dir, key+".out"))
	if err != nil || len(data) == 0 || treeStateKey(root) != key {
		return "", false, false
	}
	return string(data), true, false
}

// specRunAlive reports whether a verify for key is in progress: a background run (<key>.pid)
// or an agent's own verify (<key>.run). A marker left by a run that was killed is removed:
// waiting on it would block verify for nothing.
func specRunAlive(dir, key string) bool {
	alive := false
	for _, ext := range []string{".pid", ".run"} {
		marker := filepath.Join(dir, key+ext)
		data, err := os.ReadFile(marker)
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && processAlive(pid) {
			alive = true
			continue
		}
		_ = os.Remove(marker)
		if ext == ".pid" {
			_ = os.Remove(filepath.Join(dir, key+".tmp"))
		}
	}
	return alive
}

// beginForeground marks the agent's own verify as running for the current tree state, so a
// second verify on the same files waits for it instead of starting over, and returns done,
// which stores the output under that state (if the files did not change meanwhile): running
// verify again on unchanged files then prints it at once. Agents that background a long
// verify and lose track of it (a Haiku run looped 9 minutes waiting on a log line) can simply
// run it again.
func beginForeground(root string) (done func(out string)) {
	key := treeStateKey(root)
	if key == "" {
		return func(string) {}
	}
	dir := specDir(root)
	_ = os.MkdirAll(dir, 0755)
	marker := filepath.Join(dir, key+".run")
	_ = os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0644)
	return func(out string) {
		if out != "" && treeStateKey(root) == key {
			_ = os.WriteFile(filepath.Join(dir, key+".out"), []byte(out), 0644)
			pruneSpecResults(dir, 20)
		}
		_ = os.Remove(marker)
	}
}

// reusedNote ends a stored result printed again.
const reusedNote = "reused: same files as the verify that produced this; nothing was re-run\n"

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

// waitSpecRuns waits (up to timeout) while a background run, for any state, is in progress.
func waitSpecRuns(root string, timeout time.Duration) {
	dir := specDir(root)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := false
		marks, _ := filepath.Glob(filepath.Join(dir, "*.pid"))
		for _, m := range marks {
			if k := strings.TrimSuffix(filepath.Base(m), ".pid"); k != "watch" && specRunAlive(dir, k) {
				alive = true
			}
		}
		if !alive {
			return
		}
		time.Sleep(300 * time.Millisecond)
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
	if !speculationAllowed(root) {
		return
	}
	_ = os.MkdirAll(specDir(root), 0755)
	me := strconv.Itoa(os.Getpid())
	marker := filepath.Join(specDir(root), "watch.pid")
	_ = os.WriteFile(marker, []byte(me), 0644)
	idle := time.Hour
	if !inWorktree(root) {
		idle = 15 * time.Minute // a checkout that is not an agent's own: stop sooner
	}
	last, handled, lastChange := "", "", time.Now()
	for time.Since(lastChange) < idle {
		time.Sleep(time.Second)
		if data, err := os.ReadFile(marker); err != nil || string(data) != me {
			return // worktree removed, or a newer watcher took over
		}
		k := treeStateKey(root)
		if k != last {
			last, lastChange = k, time.Now()
		}
		// Retried every second until started: a run for an older state may still be going.
		if k != handled && maybeVerifyInBackground(root) {
			handled = k
		}
	}
	_ = os.Remove(marker)
}

// maybeWarmBaseline prepares, in the background, the baseline checkout verify compares failures
// against, when it is missing: an agent's first monoflow call (help, start, graph) comes long
// before its first failing verify, which otherwise paid ~30 s for the checkout and install.
func maybeWarmBaseline(cmd string) {
	switch cmd {
	case "init", "clone", "hook", "baseline-warm", "speculate-watch", "outer", "scenario":
		return
	}
	if os.Getenv("MONOFLOW_NO_WARM") != "" || os.Getenv("MONOFLOW_RECORD_KEY") != "" {
		return
	}
	root, err := findRepoRoot()
	if err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(root, ".agents", "monoflow.config.json")); err != nil {
		return
	}
	if verify.BaselineReady(root) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(exe, "baseline-warm")
	c.Dir = root
	c.Env = append(os.Environ(), "MONOFLOW_NO_WARM=1")
	c.SysProcAttr = detachedSysProcAttr()
	if c.Start() == nil {
		_ = c.Process.Release()
	}
}

func warmBaseline(root string) { verify.WarmBaseline(root) }

func inWorktree(root string) bool {
	return strings.Contains(filepath.ToSlash(root), "/.agents/worktrees/")
}

// speculationAllowed: agent worktrees, or any checkout whose watcher verify started
// (MONOFLOW_SPECULATE_HERE, set only for that watcher process).
func speculationAllowed(root string) bool {
	return inWorktree(root) || os.Getenv("MONOFLOW_SPECULATE_HERE") == "1"
}

// startRootWatcher starts the background watcher for a checkout that is not a worktree, when
// the agent runs verify there (agents that skip start still get their next verify precomputed
// while they edit: trpc A-14 worked in the root and its verifies ran cold, 80-125 s). Read-only
// (no formatting, no file removal), one watcher at a time, gone after 15 idle minutes.
func startRootWatcher(root string) {
	if inWorktree(root) || os.Getenv("MONOFLOW_NO_SPECULATE") != "" || os.Getenv("MONOFLOW_NO_ROOT_SPECULATE") != "" {
		return
	}
	if data, err := os.ReadFile(filepath.Join(specDir(root), "watch.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && processAlive(pid) {
			return
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "speculate-watch")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MONOFLOW_SPECULATE_HERE=1")
	cmd.SysProcAttr = detachedSysProcAttr()
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}
