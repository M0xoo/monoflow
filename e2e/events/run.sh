#!/bin/bash
# End-to-end test of MonoFlow's event harness: events from PRs, remote CI and MonoFlow's own
# loops (local CI, ship), and their delivery to agents (Claude Code hooks, opencode's text
# hooks, events unread / wait). Real repos, a real `monoflow` binary, a local bare remote;
# GitHub is a fake `gh` (e2e/events/fake-gh) and a URL rewrite, so nothing leaves the machine.
#
#   e2e/events/run.sh [path/to/monoflow]      (builds ./cmd/monoflow when no binary is given)
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
T="$(mktemp -d "${TMPDIR:-/tmp}/mf-events.XXXXXX")"
BIN="${1:-}"
if [ -z "$BIN" ]; then BIN="$T/monoflow"; (cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/monoflow) || exit 1; fi
mkdir -p "$T/fakebin" && cp "$HERE/fake-gh" "$T/fakebin/gh"
export PATH="$T/fakebin:$PATH" MONOFLOW_NO_WARM=1 MONOFLOW_NO_SPECULATE=1 MONOFLOW_FORMAT= MONOFLOW_CONFLICT_INTERVAL=2
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t

PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ok   $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  FAIL $1"; [ -n "${2:-}" ] && echo "$2" | sed 's/^/       | /' | head -25; }
check() { if eval "$2"; then ok "$1"; else bad "$1" "${3:-}"; fi; }
rawgit() { git -c core.hooksPath=/dev/null "$@"; }
mf() { local d="$1"; shift; (cd "$d" && "$BIN" "$@"); }
# waitfor <seconds> <condition>: polls a condition (this is the test, not an agent)
waitfor() { local n=$(( $1 * 2 )); shift; while [ $n -gt 0 ]; do eval "$1" && return 0; sleep 0.5; n=$((n-1)); done; return 1; }

# new_repo <name> [github]: a Go repo with one package and a test, MonoFlow installed, pushed.
new_repo() {
  local d="$T/$1"; mkdir -p "$d/remote.git" "$d/repo" "$d/other"
  git -C "$d/remote.git" init -q --bare -b main
  cd "$d/repo"
  git init -q -b main
  printf 'module example.com/app\n\ngo 1.21\n' > go.mod
  mkdir -p calc && printf 'package calc\n\nfunc Add(a, b int) int { return a + b }\n' > calc/calc.go
  printf 'package calc\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal("want 3")\n\t}\n}\n' > calc/calc_test.go
  printf 'one\n' > notes.txt
  "$BIN" init >/dev/null 2>&1
  rawgit add -A && rawgit commit -qm init
  if [ "${2:-}" = github ]; then
    git remote add origin https://github.com/acme/app.git
    git config url."$d/remote.git".insteadOf https://github.com/acme/app.git
  else
    git remote add origin "$d/remote.git"
  fi
  rawgit push -q origin main
  git -C "$d/other" clone -q "$d/remote.git" . 2>/dev/null
  export FAKE_GH="$d/gh"; mkdir -p "$FAKE_GH"; : > "$FAKE_GH/calls.log"
  cd "$T"
}
start() { mf "$T/$1/repo" start "$2" >/dev/null 2>&1; echo "$T/$1/repo/.agents/worktrees/$2"; }
deliver_text() { mf "$1" hook deliver --format=text </dev/null 2>&1; }
deliver_claude() { mf "$1" hook deliver --event=PostToolUse </dev/null 2>&1; }
stop_claude() { echo '{"stop_hook_active":false}' | (cd "$1" && "$BIN" hook stop --wait="${2:-3}") 2>&1; }
remote_has() { git -C "$T/$1/remote.git" rev-parse --verify --quiet "refs/heads/$2" >/dev/null; }

# ---------------------------------------------------------------------------------------------
echo "S1 local CI green: ship pushes; the agent hears the results once, and Stop lets it finish"
new_repo s1
W=$(start s1 green)
printf 'package calc\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n' > "$W/calc/calc.go"
out=$(mf "$W" ship -m "docs: comment Add" 2>&1)
check "ship returns at once (shipping)" '[[ "$out" == *"status: shipping"* ]]' "$out"
check "pushed once local CI is green" 'waitfor 90 "remote_has s1 agent/green"'
s=$(stop_claude "$W" 3)
check "Stop hook lets the agent finish (nothing needs action)" '[ -z "$s" ]' "$s"
d=$(deliver_text "$W")
check "delivery: local CI passed" '[[ "$d" == *OUTER_LOOP_PASSED* ]]' "$d"
check "delivery: remote CI result (non-GitHub remote: CI run locally on the pushed commit)" '[[ "$d" == *CI_PASSED* || "$d" == *CI_SKIPPED* ]]' "$d"
d2=$(deliver_text "$W")
check "nothing delivered twice" '[ -z "$d2" ]' "$d2"

# ---------------------------------------------------------------------------------------------
echo "S2 local CI red: nothing is pushed; Stop blocks with the failure; it does not loop"
new_repo s2
W=$(start s2 red)
printf 'package calc\n\nfunc Add(a, b int) int { return a - b }\n' > "$W/calc/calc.go"
mf "$W" ship -m "fix: add" >/dev/null 2>&1
s=$(stop_claude "$W" 3)
check "Stop hook blocks the agent's stop" '[[ "$s" == *"\"decision\":\"block\""* ]]' "$s"
check "the block names the failing test" '[[ "$s" == *TestAdd* || "$s" == *"want 3"* ]]' "$s"
check "the block says ship failed / local CI regressed" '[[ "$s" == *SHIP_FAILED* || "$s" == *OUTER_LOOP_REGRESSION* ]]' "$s"
check "nothing pushed" '! remote_has s2 agent/red'
s2b=$(stop_claude "$W" 1)
check "a second Stop does not block again (no loop)" '[ -z "$s2b" ]' "$s2b"

# ---------------------------------------------------------------------------------------------
echo "S3 GitHub CI red: PR opened; CI_FAILED with the failure log reaches the agent"
new_repo s3 github
W=$(start s3 gh-red)
printf 'two\n' > "$W/notes.txt"
SHA_PENDING=1
mf "$W" ship -m "chore: notes" >/dev/null 2>&1
check "pushed (local CI green)" 'waitfor 90 "remote_has s3 agent/gh-red"'
SHA=$(git -C "$T/s3/remote.git" rev-parse agent/gh-red)
echo '[{"databaseId":42,"status":"completed","conclusion":"failure","name":"ci","url":"https://github.com/acme/app/actions/runs/42","workflowName":"ci"}]' > "$FAKE_GH/runs-$SHA.json"
echo "--- FAIL: TestRemoteOnly (0.00s) remote_test.go:9: flaky backend" > "$FAKE_GH/log-42.txt"
check "PR opened through gh" 'waitfor 30 "grep -q \"pr create\" \"$FAKE_GH/calls.log\""' "$(cat "$FAKE_GH/calls.log")"
s=$(stop_claude "$W" 2)
check "Stop blocks with CI_FAILED" '[[ "$s" == *CI_FAILED* ]]' "$s"
check "the failure log is in the agent's context" '[[ "$s" == *TestRemoteOnly* ]]' "$s"

# ---------------------------------------------------------------------------------------------
echo "S4 a newer push supersedes an older CI failure: the agent never gets the stale failure"
new_repo s4 github
W=$(start s4 super)
printf 'two\n' > "$W/notes.txt"
mf "$W" ship -m "chore: notes 2" >/dev/null 2>&1
waitfor 90 "remote_has s4 agent/super"
SHA1=$(git -C "$T/s4/remote.git" rev-parse agent/super)
echo '[{"databaseId":51,"status":"completed","conclusion":"failure","name":"ci","url":"u","workflowName":"ci"}]' > "$FAKE_GH/runs-$SHA1.json"
check "first CI failure recorded" 'waitfor 40 "mf \"$W\" events list 2>/dev/null | grep -q CI_FAILED"' "$(mf "$W" events list 2>&1)"
printf 'three\n' > "$W/notes.txt"
mf "$W" ship -m "chore: notes 3" >/dev/null 2>&1
waitfor 90 '[ "$(git -C "$T/s4/remote.git" rev-parse agent/super)" != "$SHA1" ]'
SHA2=$(git -C "$T/s4/remote.git" rev-parse agent/super)
echo '[{"databaseId":52,"status":"completed","conclusion":"success","name":"ci","url":"u","workflowName":"ci"}]' > "$FAKE_GH/runs-$SHA2.json"
waitfor 40 "mf \"$W\" events list 2>/dev/null | grep -q CI_PASSED"
d=$(deliver_text "$W")
check "the newer CI_PASSED is delivered" '[[ "$d" == *CI_PASSED* ]]' "$d"
check "the superseded CI_FAILED is not delivered" '[[ "$d" != *CI_FAILED* ]]' "$d"

# ---------------------------------------------------------------------------------------------
echo "S5 conflicts with main: before the push (ship refuses) and after it (main moved)"
new_repo s5 github
W=$(start s5 conflict)
printf 'agent\n' > "$W/notes.txt"
(cd "$T/s5/other" && printf 'someone else\n' > notes.txt && rawgit commit -qam "main moved" && rawgit push -q origin main)
out=$(mf "$W" ship -m "chore: agent notes" 2>&1)
check "ship refuses a branch conflicting with origin/main" '[[ "$out" == *conflict* && "$out" == *notes.txt* ]]' "$out"
check "nothing pushed" '! remote_has s5 agent/conflict'
d=$(deliver_claude "$W")
check "PR_CONFLICT delivered (Claude JSON, additionalContext)" '[[ "$d" == *PR_CONFLICT* && "$d" == *additionalContext* ]]' "$d"
W2=$(start s5 later)
printf 'package calc\n\n// Add adds two ints.\nfunc Add(a, b int) int { return a + b }\n' > "$W2/calc/calc.go"
mf "$W2" ship -m "docs: Add" >/dev/null 2>&1
waitfor 90 "remote_has s5 agent/later"
deliver_text "$W2" >/dev/null
(cd "$T/s5/other" && rawgit pull -q origin main && printf 'package calc\n\n// Add sums.\nfunc Add(a, b int) int { return a + b }\n' > calc/calc.go && rawgit commit -qam "main edits calc" && rawgit push -q origin main)
s=$(stop_claude "$W2" 1)
check "a pushed branch that now conflicts blocks the Stop with PR_CONFLICT" '[[ "$s" == *PR_CONFLICT* && "$s" == *calc/calc.go* ]]' "$s"

# ---------------------------------------------------------------------------------------------
echo "S6 GitHub reports a conflict while CI runs (watcher): PR_CONFLICT"
new_repo s6 github
W=$(start s6 ghconf)
printf 'six\n' > "$W/notes.txt"
mf "$W" ship -m "chore: six" >/dev/null 2>&1
waitfor 90 "remote_has s6 agent/ghconf"
echo '{"number":7,"title":"chore: six","mergeable":"CONFLICTING","mergeStateStatus":"DIRTY"}' > "$FAKE_GH/pr-agent_ghconf.json"
check "watcher turns GitHub's CONFLICTING into PR_CONFLICT" 'waitfor 40 "mf \"$W\" events list 2>/dev/null | grep -q PR_CONFLICT"' "$(mf "$W" events list 2>&1)"

# ---------------------------------------------------------------------------------------------
echo "S7 events are per agent: one agent's failure never reaches another"
new_repo s7
WA=$(start s7 alpha); WB=$(start s7 beta)
printf 'package calc\n\nfunc Add(a, b int) int { return 0 }\n' > "$WA/calc/calc.go"
mf "$WA" ship -m "fix: alpha" >/dev/null 2>&1
stop_claude "$WA" 3 >/dev/null
d=$(deliver_text "$WB")
check "beta receives nothing of alpha's" '[[ "$d" != *alpha* && "$d" != *SHIP_FAILED* && "$d" != *REGRESSION* ]]' "$d"

# ---------------------------------------------------------------------------------------------
echo "S8 events wait <sha>: exit 0 on green, 1 on red (harnesses without hooks)"
new_repo s8 github
W=$(start s8 waitg)
printf 'eight\n' > "$W/notes.txt"
mf "$W" ship -m "chore: eight" >/dev/null 2>&1
waitfor 90 "remote_has s8 agent/waitg"
SHA=$(git -C "$T/s8/remote.git" rev-parse agent/waitg)
echo '[{"databaseId":81,"status":"completed","conclusion":"success","name":"ci","url":"u","workflowName":"ci"}]' > "$FAKE_GH/runs-$SHA.json"
mf "$W" events wait "$SHA" --timeout=2 >/dev/null 2>&1; rc=$?
check "events wait exits 0 when CI passed" '[ $rc -eq 0 ]' "rc=$rc"
W2=$(start s8 waitr)
printf 'eight red\n' > "$W2/notes.txt"
mf "$W2" ship -m "chore: eight red" >/dev/null 2>&1
waitfor 90 "remote_has s8 agent/waitr"
SHA=$(git -C "$T/s8/remote.git" rev-parse agent/waitr)
echo '[{"databaseId":82,"status":"completed","conclusion":"failure","name":"ci","url":"u","workflowName":"ci"}]' > "$FAKE_GH/runs-$SHA.json"
mf "$W2" events wait "$SHA" --timeout=2 >/dev/null 2>&1; rc=$?
check "events wait exits 1 when CI failed" '[ $rc -eq 1 ]' "rc=$rc"

# ---------------------------------------------------------------------------------------------
echo "S9 harness wiring: Claude settings and the opencode plugin call MonoFlow correctly"
S="$T/s1/repo/.claude/settings.json"
check "Claude settings are valid JSON" 'python3 -m json.tool "$S" >/dev/null 2>&1' "$(cat "$S" 2>&1)"
check "Claude hooks: PostToolUse delivers, Stop waits and blocks, PreToolUse redirects" 'grep -q "hook deliver" "$S" && grep -q "hook stop" "$S" && grep -q "hook pretooluse" "$S"' "$(cat "$S")"
P=$(ls "$T"/s1/repo/.opencode/plugin/*.js 2>/dev/null | head -1)
check "opencode plugin exists and parses" '[ -n "$P" ] && node --check "$P" 2>/dev/null' "$P"

# ---------------------------------------------------------------------------------------------
echo "S10 opencode: the real plugin, driven like opencode does (stub client)"
new_repo s10
W=$(start s10 oc)
printf 'package calc\n\nfunc Add(a, b int) int { return a * b }\n' > "$W/calc/calc.go"
mf "$W" ship -m "fix: add" >/dev/null 2>&1
P="$W/.opencode/plugin/$(ls "$T/s10/repo/.opencode/plugin/" | head -1)"
[ -f "$P" ] || P=$(ls "$T"/s10/repo/.opencode/plugin/*.js | head -1)
o=$(node "$HERE/opencode-drive.mjs" "$P" "$W" 2>&1)
check "idle session is prompted with the failure (hook stop through the plugin)" '[[ "$o" == *"IDLE_PROMPT<<"*TestAdd* || "$o" == *"IDLE_PROMPT<<"*SHIP_FAILED* || "$o" == *"IDLE_PROMPT<<"*REGRESSION* ]]' "$o"
check "a tool's output does not repeat what the idle prompt delivered" '[[ "$o" == *"AFTER<<tool output>>"* && "$o" == *"AFTER2<<tool output>>"* ]]' "$o"

# ---------------------------------------------------------------------------------------------
echo "S11 monoflow push refuses a branch that conflicts with origin/main"
new_repo s11
W=$(start s11 pushc)
printf 'agent\n' > "$W/notes.txt"
mf "$W" commit -m "chore: agent notes" >/dev/null 2>&1
waitfor 60 "mf \"$W\" events list 2>/dev/null | grep -q OUTER_LOOP_PASSED"
(cd "$T/s11/other" && printf 'someone else\n' > notes.txt && rawgit commit -qam "main moved" && rawgit push -q origin main)
out=$(mf "$W" push 2>&1)
check "push refused, with the conflicting file" '[[ "$out" == *conflict* && "$out" == *notes.txt* ]]' "$out"
check "nothing pushed" '! remote_has s11 agent/pushc'
d=$(deliver_text "$W")
check "the agent gets PR_CONFLICT" '[[ "$d" == *PR_CONFLICT* ]]' "$d"

# ---------------------------------------------------------------------------------------------
echo "S12 the base moves while local CI runs: the queued push is refused when it would happen"
new_repo s12
W=$(start s12 queued)
printf 'agent\n' > "$W/notes.txt"
printf 'package calc\n\nimport "time"\n\nfunc init() { time.Sleep(4 * time.Second) }\n' > "$W/calc/slow.go"
mf "$W" commit -m "chore: agent notes" >/dev/null 2>&1
out=$(mf "$W" push 2>&1)
(cd "$T/s12/other" && printf 'someone else\n' > notes.txt && rawgit commit -qam "main moved" && rawgit push -q origin main)
check "push was queued behind local CI" '[[ "$out" == *queued* ]]' "$out"
waitfor 90 "mf \"$W\" events list 2>/dev/null | grep -q PR_CONFLICT"
check "the queued push is refused: nothing pushed" '! remote_has s12 agent/queued'
d=$(deliver_text "$W")
check "the agent gets PR_CONFLICT" '[[ "$d" == *PR_CONFLICT* && "$d" == *notes.txt* ]]' "$d"

# ---------------------------------------------------------------------------------------------
echo "S13 after a push, a background watcher tells the agent when main moves into a conflict"
new_repo s13
W=$(start s13 watched)
printf 'package calc\n\n// Add adds two ints.\nfunc Add(a, b int) int { return a + b }\n' > "$W/calc/calc.go"
mf "$W" ship -m "docs: Add" >/dev/null 2>&1
waitfor 90 "remote_has s13 agent/watched"
deliver_text "$W" >/dev/null
check "a conflict watcher runs for the pushed branch" 'waitfor 10 "ls \"$T/s13/repo/.agents/worktrees/watched/.agents/conflict/\"*.pid >/dev/null 2>&1 || ls \"$T/s13/repo/.agents/conflict/\"*.pid >/dev/null 2>&1"'
(cd "$T/s13/other" && rawgit pull -q origin main && printf 'package calc\n\n// Add sums.\nfunc Add(a, b int) int { return a + b }\n' > calc/calc.go && rawgit commit -qam "main edits calc" && rawgit push -q origin main)
check "PR_CONFLICT arrives at the next tool call, with no command run" 'waitfor 30 "deliver_text \"$W\" | tee -a \"$T/s13.d\" | grep -q PR_CONFLICT" || grep -q PR_CONFLICT "$T/s13.d"' "$(cat "$T/s13.d" 2>/dev/null)"
(cd "$T/s13/other" && rawgit fetch -q origin && rawgit reset -q --hard origin/agent/watched && rawgit push -q -f origin HEAD:main)
check "the watcher stops once the branch is merged" 'waitfor 20 "! ls \"$T/s13/repo/.agents/worktrees/watched/.agents/conflict/\"*.pid >/dev/null 2>&1 && ! ls \"$T/s13/repo/.agents/conflict/\"*.pid >/dev/null 2>&1"'

echo
echo "passed $PASS, failed $FAIL   (work dir: $T)"
[ $FAIL -eq 0 ]
