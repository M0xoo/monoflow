#!/usr/bin/env bash
# A/B on a large real monorepo (opentelemetry-go, 29 Go modules): one agent per condition does a
# cross-module rename. A = MonoFlow installed, B = plain clone. Ground truth: `go vet ./...`
# (compiles code and tests) in every module of the pushed branch; a red branch goes back to its
# agent (FIX_ROUNDS, default 1), so metrics are the cost to green.
# Usage: e2e/bench/bigrepo.sh <workdir> <monoflow-binary> <out> <otel-git-dir> [model] [runs]
# TASK_FILE=e2e/bench/tasks/<name>.txt swaps the task; GT_CMD (default "go vet ./...") is the
# per-module ground-truth check, e.g. "go test ./..." for behavior changes caught only by tests.
set -uo pipefail
WORK=${1:?}; BIN=${2:?}; OUT=${3:?}; BASE=${4:?}; MODEL=${5:-opencode/big-pickle}; RUNS=${6:-2}
mkdir -p "$OUT"

TASK='Rename the exported function `StringValue` in package go.opentelemetry.io/otel/attribute
(attribute/value.go) to `StringVal`. Do not keep the old name or an alias. Update every caller in the
repository so that every Go module in it still builds and its tests pass. This repository contains many
Go modules (nested go.mod files) that depend on each other through replace directives.'
[ -n "${TASK_FILE:-}" ] && TASK=$(cat "$TASK_FILE")
GT_CMD=${GT_CMD:-go vet ./...}

PLAIN='You are an autonomous coding agent working in the opentelemetry-go repository.
Use git and the Go toolchain. Work on a branch named agent/rename, commit, and push it to origin.
Never push to main. Do not wait for or poll CI.

YOUR TASK:
'
MONO='You are an autonomous coding agent working in the opentelemetry-go repository, which is governed by
MonoFlow. Read AGENTS.md first and follow it. The monoflow CLI is ./bin/monoflow (from the repo root);
`./bin/monoflow <cmd> --help` when unsure. Never use raw git commit/push/pull/checkout. Never push to
main. Use agent id "rename". Do not wait for or poll CI.

YOUR TASK:
'

ground_truth() { # dir out-file
  local dir=$1 f=$2 br d m
  for _ in $(seq 1 60); do # queued MonoFlow pushes
    [ -z "$(ls "$dir"/src/.agents/worktrees/*/.agents/outer_loop/jobs/*.json 2>/dev/null | xargs grep -l '"status": "\(running\|queued\)"' 2>/dev/null)" ] && break
    sleep 5
  done
  : > "$f"
  for br in $(git --git-dir="$dir/remote.git" for-each-ref --format='%(refname:short)' refs/heads | grep -v '^main$'); do
    d="$dir/gt-$(echo "$br" | tr / _)"; rm -rf "$d"; git clone -q -b "$br" "$dir/remote.git" "$d"
    : > "$d.log"
    for m in $(cd "$d" && git ls-files '*go.mod' | xargs -n1 dirname); do
      grep -qxF "$m" "$WORK/base_fail.txt" && continue # already failing on main (upstream)
      (cd "$d/$m" && $GT_CMD >> "$d.log" 2>&1) || echo "FAIL $m" >> "$d.log"
    done
    if grep -q '^FAIL ' "$d.log"; then echo "$br FAIL" >> "$f"; else echo "$br PASS" >> "$f"; fi
  done
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
  (cd "$dir/src" && git remote set-url origin "$dir/remote.git" && git config user.name "agent" && git config user.email "agent@bench.invalid")
  local prompt="$PLAIN$TASK"
  if [ "$cond" = A ]; then
    (cd "$dir/src" && mkdir -p bin && ln -sf "$BIN" bin/monoflow && printf '/bin/monoflow\n/.agents/bin/monoflow\n' >> .gitignore \
      && "$BIN" init . >/dev/null && git add -A && MONOFLOW_INTERNAL=1 git commit -qm "chore: install monoflow" --no-verify \
      && MONOFLOW_INTERNAL=1 git push -q origin HEAD:main --no-verify)
    prompt="$MONO$TASK"
  fi
  s=$(date +%s)
  : > "$out/agent.jsonl"
  run_agent "$out/agent.jsonl" opencode run --dir "$dir/src" -m "$MODEL" --auto --format json "$prompt"
  echo $(( $(date +%s) - s )) > "$out/agent.secs"
  ground_truth "$dir" "$out/ci.txt"
  local round
  for round in $(seq 1 "${FIX_ROUNDS:-1}"); do
    grep -q FAIL "$out/ci.txt" || break
    local br sid; br=$(awk '$2=="FAIL"{print $1; exit}' "$out/ci.txt")
    sid=$(jq -R -r 'fromjson? | select(type=="object" and .sessionID != null) | .sessionID' "$out/agent.jsonl" | head -1)
    [ -n "$sid" ] || break
    s=$(date +%s)
    run_agent "$out/agent.jsonl" opencode run --dir "$dir/src" -s "$sid" -m "$MODEL" --auto --format json \
      "Full CI ($GT_CMD in every module) failed on your pushed branch $br:
$(grep -E '^FAIL |^--- FAIL|\.go:[0-9]+' "$dir/gt-$(echo "$br" | tr / _).log" | head -40)
Fix it and push again the same way you did before. Do not wait for CI afterwards."
    echo $(( $(cat "$out/agent.secs") + $(date +%s) - s )) > "$out/agent.secs"; echo "$round" > "$out/agent.fixes"
    ground_truth "$dir" "$out/ci.txt"
  done
  jq -R -s --arg cond "$cond" --arg run "$run" --argjson secs "$(cat "$out/agent.secs")" --argjson fixes "$(cat "$out/agent.fixes" 2>/dev/null || echo 0)" --argjson stalled "$([ -f "$out/agent.stalled" ] && echo true || echo false)" '
    [split("\n")[] | fromjson? | select(type=="object")] as $e |
    [$e[] | select(.type=="step_finish") | .part.tokens] as $t |
    { cond: $cond, run: $run, secs: $secs, fix_rounds: $fixes, stalled: $stalled, steps: ($t|length),
      tool_calls: ([$e[] | select(.type=="tool_use")] | length),
      fresh_tokens: (($t|map(.input)|add // 0) + ($t|map(.output)|add // 0)),
      cache_read: ($t|map(.cache.read // 0)|add // 0) }' "$out/agent.jsonl" > "$out/metrics.json"
}

# Free models get rate-limited for hours; a silent agent then burns AGENT_TIMEOUT per run.
if ! perl -e 'alarm shift; exec @ARGV' 120 opencode run -m "$MODEL" "reply with the single word OK" 2>/dev/null | grep -q OK; then
  echo "model $MODEL is not answering (rate-limited?): aborting" >&2; exit 2
fi

# Modules that already fail on main (upstream debt, toolchain drift) are not the agent's doing.
mkdir -p "$WORK"; : > "$WORK/base_fail.txt"
for m in $(cd "$BASE" && git ls-files '*go.mod' | xargs -n1 dirname); do
  (cd "$BASE/$m" && $GT_CMD >/dev/null 2>&1) || echo "$m" >> "$WORK/base_fail.txt"
done

for r in $(seq 1 "$RUNS"); do
  run_condition A "$r" & run_condition B "$r" & wait
done
for c in A B; do
  jq -s --arg c "$c" 'map(select(.stalled != true)) | {cond: $c, runs: length, avg_secs: (map(.secs)|add/length|floor), avg_tool_calls: (map(.tool_calls)|add/length),
    avg_fresh_tokens: (map(.fresh_tokens)|add/length|floor), fix_rounds: (map(.fix_rounds)|add)}' "$OUT"/$c-*/metrics.json
  printf '%s branches: %s\n' "$c" "$(cat "$OUT"/$c-*/ci.txt | tr '\n' ' ')"
done | tee "$OUT/summary.txt"
