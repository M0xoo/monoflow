package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// The agent golden path is four calls: start, (edit), verify, ship. Benchmarks showed MonoFlow
// agents spending ~20 calls on ceremony (help pages, worktree flags, commit, pr create, events,
// task/guard/version checks) where a plain-git agent spends ~4.

// handleStart is `monoflow start <task-id> [worktree create flags]`: an isolated worktree on
// branch agent/<task-id> (full checkout unless --packages narrows it).
func handleStart(rootDir string) {
	if len(os.Args) < 3 || strings.HasPrefix(os.Args[2], "-") {
		fmt.Println(`{"error": "usage: monoflow start <task-id>"}`)
		os.Exit(1)
	}
	os.Args = append([]string{os.Args[0], "worktree", "create"}, os.Args[2:]...)
	handleWorktree(rootDir)
}

// handleShip is `monoflow ship -m "<commit message>" [--title=<PR title>] [--body=...|--body-file=...]`:
// commit what is uncommitted (verified, outer loop started), then `pr create`, which waits for
// the outer loop, pushes and opens the PR. One call, one compact JSON.
func handleShip(rootDir string) {
	var msg, title string
	var prArgs []string
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
		default:
			fmt.Printf(`{"error": "unknown flag %q: usage: monoflow ship -m \"<commit message>\" [--title=...] [--body=...]"}`+"\n", a)
			os.Exit(1)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Printf(`{"error": %q}`+"\n", err.Error())
		os.Exit(1)
	}
	out := map[string]any{}
	if len(uncommittedFiles(rootDir)) > 0 {
		if msg == "" {
			fmt.Println(`{"error": "uncommitted changes: pass -m \"<commit message>\""}`)
			os.Exit(1)
		}
		res, ok := runSelf(exe, rootDir, "commit", "-m", msg)
		if !ok {
			res["step"] = "commit"
			printJSON(res)
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
	res, ok := runSelf(exe, rootDir, append([]string{"pr", "create", "--title=" + title}, prArgs...)...)
	for k, v := range res {
		out[k] = v
	}
	if !ok {
		out["step"] = "pr create"
		printJSON(out)
		os.Exit(1)
	}
	out["next"] = "Done: end your turn. CI results are delivered to you; act on CI_FAILED if it arrives."
	printJSON(out)
}

// runSelf runs a monoflow subcommand and parses its JSON output (the last JSON object printed).
func runSelf(exe, dir string, args ...string) (map[string]any, bool) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
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
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(data))
}
