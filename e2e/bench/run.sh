#!/usr/bin/env bash
# A/B benchmark: the same tasks done by concurrent opencode agents with MonoFlow (A) and
# with plain git + the repo's own tools (B). Ground truth = ci/run.sh on every pushed branch.
# Usage: e2e/bench/run.sh <workdir> <monoflow-binary> <out> [model] [runs]
# AGENT_TIMEOUT (seconds, default 2400) caps each agent: a rate-limited model makes opencode retry forever.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); SIM="$HERE/../sim"
WORK=${1:?}; BIN=${2:?}; OUT=${3:?}; MODEL=${4:-opencode/big-pickle}; RUNS=${5:-2}
AGENTS=(agent-billing agent-money agent-ui agent-ledger)
mkdir -p "$OUT"

plain_preamble='You are an autonomous coding agent working in the "acme" monorepo. Other agents are working on the same repository at the same time (each in its own clone).
Use git and the repo'"'"'s own tools (go test / go vet in each Go module, node test.js in each JS package, bash ci/run.sh for everything).
Work on a branch named after your agent id (e.g. agent/agent-billing), commit, and push it to origin. Never push to main. Do not wait for or poll CI.

When you are done (or blocked), print a final section exactly like:
=== AGENT REPORT ===
- outcome: <done|blocked|partial>
- friction: <one bullet per problem>
=== END REPORT ===

YOUR TASK:
'

run_condition() { # cond run
  local cond=$1 run=$2 dir="$WORK/$1-$2" out="$OUT/$1-$2"
  mkdir -p "$out"
  local repo; repo=$(bash "$SIM/setup.sh" "$dir" "$BIN" | tail -1)
  if [ "$cond" = B ]; then
    # Baseline: same repo without MonoFlow; one clone per agent.
    (cd "$repo" && git rm -rq --cached .agents AGENTS.md .claude .opencode .github 2>/dev/null; rm -rf .agents AGENTS.md .claude .opencode .github bin \
      && printf '# Acme\nGo modules (go.work) and a JS workspace. Full CI: bash ci/run.sh\n' > AGENTS.md \
      && git add -A && MONOFLOW_INTERNAL=1 git commit -qm "chore: plain repo" --no-verify && MONOFLOW_INTERNAL=1 git push -q -f origin main)
  fi
  for a in "${AGENTS[@]}"; do
    local adir="$repo" prompt
    if [ "$cond" = B ]; then
      adir="$dir/clones/$a"; git clone -q "$dir/remote.git" "$adir"
      git -C "$adir" config user.name "$a"; git -C "$adir" config user.email "$a@acme.test"
      prompt="$plain_preamble$(cat "$HERE/tasks/$a.md")"
    else
      prompt="$(cat "$SIM/tasks/_preamble.md" "$HERE/tasks/$a.md")"
    fi
    ( s=$(date +%s); perl -e 'alarm shift; exec @ARGV' "${AGENT_TIMEOUT:-2400}" opencode run --dir "$adir" -m "$MODEL" --auto --format json "$prompt" > "$out/$a.jsonl" 2>/dev/null
      echo $(( $(date +%s) - s )) > "$out/$a.secs" ) &
    sleep 2
  done
  wait
  # Let queued pushes (MonoFlow outer loop) finish, bounded.
  for _ in $(seq 1 60); do
    [ -z "$(ls "$repo"/.agents/worktrees/*/.agents/outer_loop/jobs/*.json 2>/dev/null | xargs -r grep -l '"status": "\(running\|queued\)"' 2>/dev/null)" ] && break
    sleep 5
  done
  # Ground truth on every pushed branch.
  : > "$out/ci.txt"
  for br in $(git --git-dir="$dir/remote.git" for-each-ref --format='%(refname:short)' refs/heads | grep -v '^main$'); do
    d="$dir/gt-$(echo "$br" | tr / _)"; rm -rf "$d"; git clone -q -b "$br" "$dir/remote.git" "$d"
    if (cd "$d" && bash ci/run.sh >/dev/null 2>&1); then echo "$br PASS" >> "$out/ci.txt"; else echo "$br FAIL" >> "$out/ci.txt"; fi
  done
  # Per-agent metrics.
  for a in "${AGENTS[@]}"; do
    jq -s --arg a "$a" --arg cond "$cond" --arg run "$run" --argjson secs "$(cat "$out/$a.secs")" '
      [.[] | select(.type=="step_finish") | .part.tokens] as $t |
      { cond: $cond, run: $run, agent: $a, secs: $secs,
        steps: ($t|length),
        tool_calls: ([.[] | select(.type=="tool_use")] | length),
        input: ($t|map(.input)|add // 0), output: ($t|map(.output)|add // 0),
        cache_read: ($t|map(.cache.read // 0)|add // 0) }' "$out/$a.jsonl"
  done > "$out/metrics.jsonl"
}

for r in $(seq 1 "$RUNS"); do
  run_condition A "$r" & run_condition B "$r" & wait
done
jq -s 'group_by(.cond) | map({cond: .[0].cond, agents: length,
  avg_secs: (map(.secs)|add/length|floor), avg_tool_calls: (map(.tool_calls)|add/length*10|floor/10),
  avg_steps: (map(.steps)|add/length*10|floor/10),
  avg_fresh_tokens: (map(.input + .output)|add/length|floor), avg_total_context: (map(.input + .output + .cache_read)|add/length|floor)})' "$OUT"/*/metrics.jsonl > "$OUT/summary.json"
cat "$OUT"/*/ci.txt | awk '{print FILENAME? $0 : $0}' > /dev/null
for c in A B; do printf '%s pushed=%s red=%s\n' "$c" "$(cat "$OUT"/$c-*/ci.txt | wc -l | tr -d ' ')" "$(cat "$OUT"/$c-*/ci.txt | grep -c FAIL)"; done > "$OUT/ci_summary.txt"
cat "$OUT/summary.json" "$OUT/ci_summary.txt"
