package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/antigravity/monoflow/pkg/events"
)

// The agent golden path is four calls: start, (edit), verify, ship. Benchmarks showed MonoFlow
// agents spending ~20 calls on ceremony (help pages, worktree flags, commit, pr create, events,
// task/guard/version checks) where a plain-git agent spends ~4.

// handleStart is `monoflow start <task-id> [worktree create flags]`: an isolated worktree on
// branch agent/<task-id> (full checkout unless --packages narrows it).
func handleStart(rootDir string) {
	if len(os.Args) < 3 || strings.HasPrefix(os.Args[2], "-") {
		printRaw(`{"error": "usage: monoflow start <task-id>"}`)
		os.Exit(1)
	}
	id := os.Args[2]
	os.Args = append([]string{os.Args[0], "worktree", "create"}, os.Args[2:]...)
	handleWorktree(rootDir)
	startSpeculateWatcher(filepath.Join(rootDir, ".agents", "worktrees", id))
}

// handleShip is `monoflow ship -m "<commit message>" [--title=<PR title>] [--body=...|--body-file=...]`:
// commit what is uncommitted (verified, outer loop started), then `pr create`, which waits for
// the outer loop, pushes and opens the PR. One call, one compact JSON.
func handleShip(rootDir string) {
	var msg, title string
	var prArgs []string
	wait, deliver := false, false
	for i := 2; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case a == "-m" && i+1 < len(os.Args):
			msg = os.Args[i+1]
			i++
		case strings.HasPrefix(a, "-m="), strings.HasPrefix(a, "--message="):
			msg = a[strings.Index(a, "=")+1:]
		case strings.HasPrefix(a, "--title="):
			title = strings.TrimPrefix(a, "--title=")
		case strings.HasPrefix(a, "--body="), strings.HasPrefix(a, "--body-file="), a == "--verbose":
			prArgs = append(prArgs, a)
		case a == "--wait":
			wait = true
		case a == "--deliver": // internal: the detached second half of ship
			deliver = true
		default:
			printRaw(fmt.Sprintf(`{"error": "unknown flag %q: usage: monoflow ship -m \"<commit message>\" [--title=...] [--body=...]"}`, a))
			os.Exit(1)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		printRaw(fmt.Sprintf(`{"error": %q}`, err.Error()))
		os.Exit(1)
	}
	if deliver {
		shipDeliver(exe, rootDir, title, prArgs)
		return
	}
	out := map[string]any{}
	// Called on main with uncommitted work (the agent skipped start, or explored first): move
	// that work onto its own branch and worktree instead of making the agent do it by hand
	// (Haiku: git diff, start, git apply, git restore: 4 calls).
	if b := currentBranch(rootDir); (b == "main" || b == "master") && len(uncommittedFiles(rootDir)) > 0 {
		if msg == "" {
			printRaw(`{"error": "uncommitted changes: pass -m \"<commit message>\""}`)
			os.Exit(1)
		}
		wt, err := moveWorkToWorktree(exe, rootDir, msg)
		if err != nil {
			printRaw(fmt.Sprintf(`{"error": %q}`, "could not move your changes off "+b+": "+err.Error()))
			os.Exit(1)
		}
		rootDir = wt
		out["moved_to"] = wt
	}
	if len(uncommittedFiles(rootDir)) > 0 {
		if msg == "" {
			printRaw(`{"error": "uncommitted changes: pass -m \"<commit message>\""}`)
			os.Exit(1)
		}
		res, ok := runSelf(exe, rootDir, "commit", "-m", msg)
		if !ok {
			res["step"] = "commit"
			printOut(res)
			os.Exit(1)
		}
		if c, ok := res["commit"].(map[string]any); ok {
			out["commit"] = c["commit_sha"]
		}
	}
	if title == "" {
		title = strings.SplitN(strings.TrimSpace(msg), "\n", 2)[0]
	}
	if title == "" {
		if b, err := exec.Command("git", "-C", rootDir, "log", "-1", "--format=%s").Output(); err == nil {
			title = strings.TrimSpace(string(b))
		}
	}
	if !wait {
		// Push and PR happen once the outer loop (local CI on this commit) is green, in the
		// background: the agent is free at once, as after a plain git push (ship used to block
		// ~25 s on otel while local CI ran). Failures arrive as a SHIP_FAILED event.
		if err := spawnShipDeliver(exe, rootDir, title, prArgs); err == nil {
			out["status"] = "shipping"
			out["next"] = "Done: end your turn. Local CI is running on this commit; it pushes and opens the PR when green. A failure is delivered to you (SHIP_FAILED / OUTER_LOOP_REGRESSION)."
			printOut(out)
			return
		}
	}
	res, ok := runSelf(exe, rootDir, append([]string{"pr", "create", "--title=" + title}, prArgs...)...)
	for k, v := range res {
		out[k] = v
	}
	if !ok {
		out["step"] = "pr create"
		printOut(out)
		os.Exit(1)
	}
	out["next"] = "Done: end your turn. CI results are delivered to you; act on CI_FAILED if it arrives."
	printOut(out)
}

// runSelf runs a monoflow subcommand and parses its JSON output (the last JSON object printed).
func runSelf(exe, dir string, args ...string) (map[string]any, bool) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MONOFLOW_FORMAT=json") // parsed below
	raw, err := cmd.Output()
	res := map[string]any{}
	text := strings.TrimSpace(string(raw))
	if i := strings.Index(text, "{"); i >= 0 {
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		for dec.More() {
			var m map[string]any
			if dec.Decode(&m) != nil {
				break
			}
			for k, v := range m {
				res[k] = v
			}
		}
	}
	if len(res) == 0 {
		res["output"] = text
	}
	if s, _ := res["status"].(string); s == "blocked" || s == "failed" || s == "push_failed" || res["error"] != nil {
		return res, false
	}
	return res, err == nil
}

func printJSON(v any) {
	printOut(v)
}

// spawnShipDeliver starts `monoflow ship --deliver` detached, logging to .agents/ship/.
func spawnShipDeliver(exe, rootDir, title string, prArgs []string) error {
	dir := filepath.Join(rootDir, ".agents", "ship")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "ship.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(exe, append([]string{"ship", "--deliver", "--title=" + title}, prArgs...)...)
	cmd.Dir = rootDir
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// shipDeliver is the background half of ship: pr create (waits for the outer loop, pushes, opens
// the PR); a failure becomes a SHIP_FAILED event. A marker file tells hooks a ship is in flight.
func shipDeliver(exe, rootDir, title string, prArgs []string) {
	marker := filepath.Join(rootDir, ".agents", "ship", fmt.Sprintf("%d.inflight", os.Getpid()))
	_ = os.WriteFile(marker, nil, 0644)
	defer os.Remove(marker)
	res, ok := runSelf(exe, rootDir, append([]string{"pr", "create", "--title=" + title}, prArgs...)...)
	printJSON(res)
	if ok {
		return
	}
	sha, _ := exec.Command("git", "-C", rootDir, "rev-parse", "HEAD").Output()
	reason, _ := res["error"].(string)
	if reason == "" {
		reason, _ = res["status"].(string)
	}
	_ = events.NewEventManager(rootDir).Emit(&events.Event{
		ID:             fmt.Sprintf("evt_ship_failed_%d", time.Now().UnixNano()),
		Type:           events.EventShipFailed,
		CommitSHA:      strings.TrimSpace(string(sha)),
		Branch:         currentBranch(rootDir),
		CreatedAt:      time.Now().UTC(),
		RequiresAction: true,
		Summary:        "ship could not push / open the PR: " + reason,
		SuggestedTask:  "Fix what it reports, 'monoflow verify', then 'monoflow ship -m \"<msg>\"' again (or 'monoflow ship --wait' to see it inline)",
	})
}

// moveWorkToWorktree creates a worktree and branch named after the commit message (via
// `monoflow start`), moves the uncommitted changes of rootDir (tracked and new files) into it,
// and leaves rootDir clean. Returns the worktree path.
func moveWorkToWorktree(exe, rootDir, msg string) (string, error) {
	git := func(dir string, args ...string) ([]byte, error) {
		return exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	}
	diff, err := git(rootDir, "diff", "HEAD", "--binary")
	if err != nil {
		return "", err
	}
	untrackedRaw, _ := git(rootDir, "ls-files", "--others", "--exclude-standard", "-z")
	var untracked []string
	for _, f := range strings.Split(strings.TrimRight(string(untrackedRaw), "\x00"), "\x00") {
		if f != "" && !strings.HasPrefix(f, ".agents/") && f != "bin/monoflow" {
			untracked = append(untracked, f)
		}
	}
	id := shipID(msg)
	res, ok := runSelf(exe, rootDir, "start", id)
	wt, _ := res["path"].(string)
	if !ok || wt == "" {
		return "", fmt.Errorf("start %s: %v", id, res["error"])
	}
	if len(diff) > 0 {
		apply := exec.Command("git", "-C", wt, "apply", "--binary", "-")
		apply.Stdin = strings.NewReader(string(diff))
		if out, err := apply.CombinedOutput(); err != nil {
			return "", fmt.Errorf("apply to %s: %v (%s)", wt, err, strings.TrimSpace(string(out)))
		}
	}
	for _, f := range untracked {
		data, err := os.ReadFile(filepath.Join(rootDir, f))
		if err != nil {
			continue
		}
		dst := filepath.Join(wt, f)
		_ = os.MkdirAll(filepath.Dir(dst), 0755)
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return "", err
		}
	}
	// Only now that the worktree holds the work: clean the root.
	if len(diff) > 0 {
		if out, err := exec.Command("git", "-C", rootDir, "checkout", "HEAD", "--", ".").CombinedOutput(); err != nil {
			return "", fmt.Errorf("restore %s: %v (%s)", rootDir, err, strings.TrimSpace(string(out)))
		}
	}
	for _, f := range untracked {
		_ = os.Remove(filepath.Join(rootDir, f))
	}
	return wt, nil
}

// shipID derives a worktree/branch id from the commit message: "codes: use upper-case names"
// -> "codes-use-upper-case-<time>".
func shipID(msg string) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(strings.SplitN(msg, "\n", 2)[0]), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		words = append(words, w)
		if len(words) == 4 {
			break
		}
	}
	return strings.Join(append(words, fmt.Sprintf("%x", time.Now().Unix()%0xfffff)), "-")
}
