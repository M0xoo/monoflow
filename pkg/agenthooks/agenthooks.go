// Package agenthooks steers coding agents onto MonoFlow commands at the harness level.
// Rules in AGENTS.md are routinely ignored; a PreToolUse hook that denies the raw command
// and names the MonoFlow replacement makes the agent self-correct on its next call.
package agenthooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Rule maps a raw command to its MonoFlow replacement.
type Rule struct {
	Pattern     *regexp.Regexp
	Allow       *regexp.Regexp // exceptions (e.g. focused test runs)
	Replacement string
	Why         string
	// Soft rules (builds/checks) only nudge: the first attempt is bounced with the MonoFlow
	// alternative and an identical retry is allowed, so agents prefer MonoFlow but can
	// still run raw tools when they really need them. Hard rules guard commands that would
	// bypass a safety gate (commit/push/PR/branch switching).
	Soft bool
}

// segment start: beginning of command or after a shell separator, optional env assignments.
const seg = `(?:^|[;&|(]\s*|\bthen\s+|\bdo\s+)(?:[A-Z_][A-Z0-9_]*=\S*\s+)*`

var Rules = []Rule{
	{regexp.MustCompile(seg + `git\s+(?:-C\s+\S+\s+)?commit\b`), nil, `monoflow commit -m "<msg>"`, "runs the test-integrity guard and never commits MonoFlow runtime state", false},
	{regexp.MustCompile(seg + `git\s+(?:-C\s+\S+\s+)?push\b`), nil, "monoflow push", "verifies affected packages first so remote CI cannot fail, then starts the CI watcher", false},
	{regexp.MustCompile(seg + `git\s+(?:-C\s+\S+\s+)?pull\b`), nil, "monoflow sync", "keeps the sparse package subset consistent", false},
	{regexp.MustCompile(seg + `git\s+(?:-C\s+\S+\s+)?(?:checkout|switch)\b`), regexp.MustCompile(`git\s+(?:-C\s+\S+\s+)?checkout\s+(?:\S+\s+)?--\s`), "monoflow worktree create <agent-id> --packages=<p> (or monoflow branch <name>)", "switching branches in a shared checkout breaks other agents", false},
	{regexp.MustCompile(seg + `gh\s+pr\s+create\b`), nil, `monoflow pr create --title="..."`, "PRs are only opened after the Definition-of-Done gate passes", false},
	{regexp.MustCompile(seg + `gh\s+(?:run\s+watch|pr\s+checks)\b`), nil, "monoflow events unread (then end your turn)", "CI results arrive as events; polling burns context", false},
	{regexp.MustCompile(seg + `go\s+(?:test|build|vet)\b`), regexp.MustCompile(`go\s+test\b.*\s-run[\s=]`), "monoflow verify --affected (or --package=<p> --test)", "covers dependents, caches results and returns compact JSON", true},
	{regexp.MustCompile(seg + `(?:npm|pnpm|yarn|bun)\s+(?:run\s+)?(?:test|build|lint|typecheck)\b`), nil, "monoflow verify --affected", "covers dependents, caches results and returns compact JSON", true},
	{regexp.MustCompile(seg + `(?:npx\s+)?(?:tsc|jest|vitest|eslint|pytest|mypy|ruff)\b`), regexp.MustCompile(`(?:jest|vitest|pytest)\b.*\s(?:-t|-k|--testNamePattern)[\s=]`), "monoflow verify --affected", "covers dependents, caches results and returns compact JSON", true},
	{regexp.MustCompile(seg + `cargo\s+(?:test|build|check|clippy)\b`), nil, "monoflow verify --affected", "covers dependents, caches results and returns compact JSON", true},
}

// Check returns the matching rule for a shell command, or nil when it is allowed.
func Check(command string) *Rule {
	if strings.Contains(command, "MONOFLOW_ALLOW_RAW=1") {
		return nil
	}
	for i := range Rules {
		r := &Rules[i]
		if r.Pattern.MatchString(command) && (r.Allow == nil || !r.Allow.MatchString(command)) {
			return r
		}
	}
	return nil
}

// Reason formats the message shown to the agent.
func (r *Rule) Reason() string {
	if r.Soft {
		return "MonoFlow tip: prefer `" + r.Replacement + "` — it " + r.Why + ". If you really need the raw command, re-run it unchanged and it will be allowed."
	}
	return "MonoFlow: use `" + r.Replacement + "` instead — it " + r.Why + ". (Prefix with MONOFLOW_ALLOW_RAW=1 only if MonoFlow cannot do this.)"
}

const nudgeWindow = 10 * time.Minute

// Decide returns the rule to enforce for command, or nil to allow it. Soft rules deny only
// the first attempt of a given command within nudgeWindow (state kept under stateDir).
func Decide(stateDir, command string) *Rule {
	r := Check(command)
	if r == nil || !r.Soft || stateDir == "" {
		return r
	}
	path := filepath.Join(stateDir, "nudges.json")
	seen := map[string]time.Time{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	key := hex.EncodeToString(sum[:8])
	now := time.Now()
	for k, t := range seen {
		if now.Sub(t) > nudgeWindow {
			delete(seen, k)
		}
	}
	if _, nudged := seen[key]; nudged {
		return nil
	}
	seen[key] = now
	if data, err := json.Marshal(seen); err == nil {
		_ = os.MkdirAll(stateDir, 0755)
		_ = os.WriteFile(path, data, 0644)
	}
	return r
}

// ClaudePreToolUse handles a Claude Code PreToolUse hook payload. It returns the JSON to
// print (empty when allowed).
func ClaudePreToolUse(in io.Reader, stateDir string) []byte {
	var payload struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(in).Decode(&payload); err != nil || payload.ToolName != "Bash" {
		return nil
	}
	r := Decide(stateDir, payload.ToolInput.Command)
	if r == nil {
		return nil
	}
	out, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": r.Reason(),
		},
	})
	return out
}

// ClaudeSettingsHook is the .claude/settings.json hook entry installed by `monoflow init`.
func ClaudeSettingsHook() map[string]any {
	return map[string]any{
		"matcher": "Bash",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": `"$CLAUDE_PROJECT_DIR/.agents/bin/monoflow" hook pretooluse`,
		}},
	}
}

// ClaudeEventHooks are the Claude Code hooks that deliver MonoFlow events to the agent
// without it running a command: new events are injected as context at session start, on
// every prompt and after every tool call; the Stop hook waits for in-flight outer-loop/CI
// results and blocks the stop when something needs the agent's action.
func ClaudeEventHooks() map[string]map[string]any {
	bin := `"$CLAUDE_PROJECT_DIR/.agents/bin/monoflow"`
	entry := func(matcher, cmd string, timeout int) map[string]any {
		h := map[string]any{"type": "command", "command": cmd}
		if timeout > 0 {
			h["timeout"] = timeout
		}
		e := map[string]any{"hooks": []any{h}}
		if matcher != "" {
			e["matcher"] = matcher
		}
		return e
	}
	return map[string]map[string]any{
		"SessionStart":     entry("", bin+" hook deliver --event=SessionStart", 0),
		"UserPromptSubmit": entry("", bin+" hook deliver --event=UserPromptSubmit", 0),
		"PostToolUse":      entry("*", bin+" hook deliver --event=PostToolUse", 0),
		"Stop":             entry("", bin+" hook stop", 1200),
	}
}

// OpencodePlugin is the opencode plugin installed by `monoflow init` (.opencode/plugin/monoflow.js).
const OpencodePlugin = `// Installed by monoflow init (managed: re-running init updates it).
// - redirects raw build/test/git commands to MonoFlow (tool.execute.before)
// - delivers MonoFlow events (CI, outer loop, releases) into tool output (tool.execute.after)
// - when the session goes idle, waits for in-flight outer-loop/CI results and prompts the
//   session with anything that needs action, so the agent resumes without running a command
import { execFile, execFileSync } from "node:child_process";
import { join } from "node:path";

export const MonoflowGuard = async ({ client, directory }) => {
  const bin = join(directory, ".agents", "bin", "monoflow");
  let idleWatch = null;
  return {
    "tool.execute.before": async (input, output) => {
      if (input.tool !== "bash") return;
      const command = output.args?.command ?? "";
      try {
        execFileSync(bin, ["hook", "check"], { input: command, stdio: ["pipe", "pipe", "pipe"] });
      } catch (e) {
        if (e.status === 2) throw new Error(String(e.stdout || e.message).trim());
      }
    },
    "tool.execute.after": async (input, output) => {
      try {
        const text = execFileSync(bin, ["hook", "deliver", "--format=text"], { cwd: directory, encoding: "utf8", input: "" });
        if (text.trim()) output.output = (output.output ?? "") + "\n\n" + text;
      } catch {}
    },
    event: async ({ event }) => {
      if (event.type !== "session.idle" || idleWatch) return;
      const sessionID = event.properties?.sessionID;
      idleWatch = execFile(bin, ["hook", "stop", "--format=text"], { cwd: directory, encoding: "utf8" }, async (err, stdout) => {
        idleWatch = null;
        if (stdout && stdout.trim() && sessionID) {
          await client.session.prompt({ path: { id: sessionID }, body: { parts: [{ type: "text", text: stdout }] } });
        }
      });
      idleWatch.stdin?.end(); // hook stop reads (optional) JSON on stdin: close it
    },
  };
};
`
