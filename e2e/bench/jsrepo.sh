#!/usr/bin/env bash
# A/B on a real JS/TS monorepo that already has its own task runner (TanStack Query: Nx,
# trpc: Turborepo). One agent per condition does a behaviour change whose breakage shows up in
# other packages' tests. A = MonoFlow installed, B = the repo as-is (pnpm + Nx/Turbo, which
# already offer "affected" commands). Ground truth: GT_CMD (the repo's own CI-style checks) on
# the pushed branch; only failures absent on main count (flaky/preexisting ones are ignored).
# A red branch goes back to its agent (FIX_ROUNDS, default 1): metrics are the cost to green.
# Usage: e2e/bench/jsrepo.sh <workdir> <monoflow-binary> <out> <repo-dir (installed)> <task-file> [model] [runs]
# Env: GT_CMD (required), AGENT_TIMEOUT (default 2400), FIX_ROUNDS (default 1).
set -uo pipefail
WORK=${1:?}; BIN=${2:?}; OUT=${3:?}; BASE=${4:?}; TASK_FILE=${5:?}; MODEL=${6:-opencode/big-pickle}; RUNS=${7:-2}
: "${GT_CMD:?set GT_CMD to the CI checks of the repo}"
mkdir -p "$OUT" "$WORK"
TASK=$(cat "$TASK_FILE")
NAME=$(basename "$BASE")

PLAIN="You are an autonomous coding agent working in the $NAME repository (a pnpm monorepo).
Use git, pnpm and the repository's own tooling. Work on a branch named agent/task, commit, and push it
to origin. Never push to main. Do not wait for or poll CI.

YOUR TASK:
"
MONO="You are an autonomous coding agent working in the $NAME repository (a pnpm monorepo), which is
governed by MonoFlow. Read AGENTS.md first and follow it. The monoflow CLI is ./bin/monoflow (from the
repo root); \`./bin/monoflow <cmd> --help\` when unsure. Never use raw git commit/push/pull/checkout.
Never push to main. Use agent id \"task\". Do not wait for or poll CI.

YOUR TASK:
"

# failures <log> <checkout>: one line per failing test / type error, checkout path removed.
failures() {
  grep -E '^ ?FAIL |error TS[0-9]+' "$1" | sed -e "s#$2##g" -e 's/([0-9.]*m\?s)//g' -e 's/[[:space:]]*$//' | sort -u
}

ground_truth() { # dir out-file
  local dir=$1 f=$2 br d
  for _ in $(seq 1 120); do # queued MonoFlow pushes
    [ -z "$(ls "$dir"/src/.agents/worktrees/*/.agents/outer_loop/jobs/*.json 2>/dev/null | xargs grep -l '"status": "\(running\|queued\)"' 2>/dev/null)" ] && break
    sleep 5
  done
  : > "$f"
  for br in $(git --git-dir="$dir/remote.git" for-each-ref --format='%(refname:short)' refs/heads | grep -v '^main$'); do
    d="$dir/gt-$(echo "$br" | tr / _)"; rm -rf "$d"; git clone -q -b "$br" "$dir/remote.git" "$d"
    (cd "$d" && pnpm install --frozen-lockfile --prefer-offline >/dev/null 2>&1 && sh -c "$GT_CMD") > "$d.log" 2>&1
    failures "$d.log" "$d" > "$d.fail"
    comm -13 "$WORK/base.fail" "$d.fail" > "$d.new"
    if [ -s "$d.new" ]; then echo "$br FAIL $(wc -l < "$d.new")" >> "$f"; else echo "$br PASS" >> "$f"; fi
  done
}

# agent_cli <workdir> <session-or-empty> <prompt>: the coding agent under test. AGENT=opencode
# (default; MODEL like opencode/big-pickle) or AGENT=cursor (Cursor CLI; MODEL like auto), whose
# stream-json reports real token usage (opencode's cursor-acp bridge reports 0).
agent_cli() {
  local d=$1 sid=$2 p=$3
  if [ "${AGENT:-opencode}" = cursor ]; then
    (cd "$d" && agent -p --force --trust --output-format stream-json --model "$MODEL" ${sid:+--resume "$sid"} "$p")
  else
    opencode run --dir "$d" ${sid:+-s "$sid"} -m "$MODEL" --auto --format json "$p"
  fi
}

# run_agent <jsonl> <cmd...>: run an agent, appending its JSON events to <jsonl>. Free models
# stall mid-run (rate limits) without exiting: no new events for STALL_SECS (default 600) kills
# it and marks the run stalled, so it is excluded instead of counted as a 30-minute failure.
run_agent() {
  local f=$1; shift
  "$@" >> "$f" 2>/dev/null &
  local pid=$! last=-1 idle=0 el=0 sz
  while kill -0 "$pid" 2>/dev/null; do
    sleep 20; el=$((el + 20)); sz=$(wc -c < "$f")
    if [ "$sz" = "$last" ]; then idle=$((idle + 20)); else idle=0; last=$sz; fi
    if [ "$idle" -ge "${STALL_SECS:-600}" ] || [ "$el" -ge "${AGENT_TIMEOUT:-2400}" ]; then
      [ "$idle" -ge "${STALL_SECS:-600}" ] && echo "stalled after ${el}s" > "${f%.jsonl}.stalled"
      pkill -P "$pid" 2>/dev/null; kill "$pid" 2>/dev/null; break
    fi
  done
  wait "$pid" 2>/dev/null
}

run_condition() { # cond run
  local cond=$1 run=$2 dir="$WORK/$1-$2" out="$OUT/$1-$2"
  rm -rf "$dir"; mkdir -p "$dir" "$out"; rm -f "$out"/*
  git clone -q "$BASE" "$dir/src" && git clone -q --bare "$dir/src" "$dir/remote.git"
  (cd "$dir/src" && git remote set-url origin "$dir/remote.git" && git config user.name "agent" && git config user.email "agent@bench.invalid" \
    && pnpm install --frozen-lockfile --prefer-offline >/dev/null 2>&1)
  local prompt="$PLAIN$TASK"
  if [ "$cond" = A ]; then
    (cd "$dir/src" && mkdir -p bin && ln -sf "$BIN" bin/monoflow && printf '/bin/monoflow\n/.agents/bin/monoflow\n' >> .gitignore \
      && "$BIN" init . >/dev/null && git add -A && MONOFLOW_INTERNAL=1 git commit -qm "chore: install monoflow" --no-verify \
      && MONOFLOW_INTERNAL=1 git push -q origin HEAD:main --no-verify)
    prompt="$MONO$TASK"
  fi
  s=$(date +%s)
  : > "$out/agent.jsonl"
  run_agent "$out/agent.jsonl" agent_cli "$dir/src" "" "$prompt"
  echo $(( $(date +%s) - s )) > "$out/agent.secs"
  ground_truth "$dir" "$out/ci.txt"
  local round
  for round in $(seq 1 "${FIX_ROUNDS:-1}"); do
    grep -q FAIL "$out/ci.txt" || break
    local br sid; br=$(awk '$2=="FAIL"{print $1; exit}' "$out/ci.txt")
    sid=$(jq -R -r 'fromjson? | select(type=="object") | (.sessionID // .session_id // empty)' "$out/agent.jsonl" | head -1)
    [ -n "$sid" ] || break
    s=$(date +%s)
    run_agent "$out/agent.jsonl" agent_cli "$dir/src" "$sid" \
      "Full CI failed on your pushed branch $br. New failures (not on main):
$(head -40 "$dir/gt-$(echo "$br" | tr / _).new")
Fix them and push again the same way you did before. Do not wait for CI afterwards."
    echo $(( $(cat "$out/agent.secs") + $(date +%s) - s )) > "$out/agent.secs"; echo "$round" > "$out/agent.fixes"
    ground_truth "$dir" "$out/ci.txt"
  done
  jq -R -s --arg cond "$cond" --arg run "$run" --argjson secs "$(cat "$out/agent.secs")" --argjson fixes "$(cat "$out/agent.fixes" 2>/dev/null || echo 0)" --argjson stalled "$([ -f "$out/agent.stalled" ] && echo true || echo false)" '
    [split("\n")[] | fromjson? | select(type=="object")] as $e |
    [$e[] | select(.type=="step_finish") | .part.tokens] as $t |
    [$e[] | select(.type=="result" and .usage != null) | .usage] as $u |
    { cond: $cond, run: $run, secs: $secs, fix_rounds: $fixes, stalled: $stalled, steps: ($t|length),
      tool_calls: ([$e[] | select(.type=="tool_use" or (.type=="tool_call" and .subtype=="started"))] | length),
      fresh_tokens: (($t|map(.input)|add // 0) + ($t|map(.output)|add // 0) + ($u|map(.inputTokens + .outputTokens)|add // 0)),
      cache_read: (($t|map(.cache.read // 0)|add // 0) + ($u|map(.cacheReadTokens // 0)|add // 0)) }' "$out/agent.jsonl" > "$out/metrics.json"
}

# Free models get rate-limited for hours; a silent agent then burns AGENT_TIMEOUT per run.
model_ok() { perl -e 'alarm shift; exec @ARGV' 120 bash -c "$(declare -f agent_cli); MODEL='$MODEL' AGENT='${AGENT:-opencode}' agent_cli . '' 'reply with the single word OK'" 2>/dev/null | grep -q OK; }
if ! { model_ok || { sleep 20; model_ok; } || { sleep 60; model_ok; }; }; then
  echo "model $MODEL is not answering (rate-limited?): aborting" >&2; exit 2
fi

# Failures main already has (flaky tests, toolchain drift) are not the agent's doing.
if [ ! -s "$WORK/base.fail" ]; then
  (cd "$BASE" && sh -c "$GT_CMD") > "$WORK/base.log" 2>&1
  failures "$WORK/base.log" "$BASE" > "$WORK/base.fail"
fi

for r in $(seq 1 "$RUNS"); do
  run_condition A "$r" & run_condition B "$r" & wait
done
for c in A B; do
  jq -s --arg c "$c" 'map(select(.stalled != true)) | {cond: $c, runs: length, avg_secs: (map(.secs)|add/length|floor), avg_tool_calls: (map(.tool_calls)|add/length),
    avg_fresh_tokens: (map(.fresh_tokens)|add/length|floor), fix_rounds: (map(.fix_rounds)|add)}' "$OUT"/$c-*/metrics.json
  printf '%s branches: %s\n' "$c" "$(cat "$OUT"/$c-*/ci.txt | tr '\n' ' ')"
done | tee "$OUT/summary.txt"
