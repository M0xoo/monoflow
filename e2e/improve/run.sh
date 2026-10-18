#!/usr/bin/env bash
# One round of MonoFlow's improvement loop: concurrent opencode agents do real tasks on the sim
# repo with MonoFlow; every session ends with `monoflow improve` (a retro on MonoFlow itself);
# then the suggestions are triaged. Fix what triage finds, rebuild, run the next round.
# Usage: e2e/improve/run.sh <workdir> <monoflow-binary> <out> [model] [agents...]
# Suggestions and triage decisions accumulate across rounds in $MONOFLOW_IMPROVE_DIR
# (default <workdir>.store), so a problem marked fixed and reported again reopens.
# Headless `opencode run` has no Stop hook: when an agent ends without a retro, the harness
# resumes its session with what the Stop hook would have said (counted as "prompted").
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); SIM="$HERE/../sim"; BENCH="$HERE/../bench"
WORK=${1:?workdir}; BIN=${2:?monoflow binary}; OUT=${3:?out}; MODEL=${4:-opencode/nemotron-3-ultra-free}; shift 4 || shift $#
AGENTS=("$@"); [ ${#AGENTS[@]} -eq 0 ] && AGENTS=(agent-billing agent-money agent-ui agent-ledger agent-api)
export MONOFLOW_IMPROVE_DIR=${MONOFLOW_IMPROVE_DIR:-$WORK.store}
mkdir -p "$OUT" "$MONOFLOW_IMPROVE_DIR"
since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
REPO=$(bash "$SIM/setup.sh" "$WORK" "$BIN" | tail -1)

task_of() { [ -f "$BENCH/tasks/$1.md" ] && cat "$BENCH/tasks/$1.md" || cat "$SIM/tasks/$1.md"; }
has_retro() { [ -n "$(jq -r --arg a "$1" --arg s "$since" 'select(.agent == $a and .ts > $s) | .ts' "$MONOFLOW_IMPROVE_DIR/retros.jsonl" 2>/dev/null)" ]; }
oc() { perl -e 'alarm shift; exec @ARGV' "${AGENT_TIMEOUT:-1500}" opencode run --dir "$REPO" -m "$MODEL" --auto --format json "$@"; }

for a in "${AGENTS[@]}"; do
  ( export MONOFLOW_AGENT=$a; s=$(date +%s)
    oc "$(cat "$SIM/tasks/_preamble.md"; task_of "$a")" > "$OUT/$a.jsonl" 2>"$OUT/$a.stderr"
    prompted=0
    if ! has_retro "$a"; then
      # What the Stop hook says at the end of a Claude Code session.
      wt="$REPO"; [ -d "$REPO/.agents/worktrees/$a" ] && wt="$REPO/.agents/worktrees/$a"
      sid=$(jq -r 'select(.sessionID != null) | .sessionID' "$OUT/$a.jsonl" | head -1)
      if [ -n "$sid" ]; then
        prompted=1
        msg="Before you finish: a 1-minute retro on MonoFlow helps fix what got in your way. Run it from $wt:
$(cd "$wt" && "$REPO/bin/monoflow" improve 2>&1)"
        oc -s "$sid" "$msg" >> "$OUT/$a.jsonl" 2>>"$OUT/$a.stderr"
      fi
    fi
    jq -n --arg a "$a" --argjson secs $(( $(date +%s) - s )) --argjson prompted $prompted \
      --argjson retro "$(has_retro "$a" && echo true || echo false)" \
      '{agent: $a, secs: $secs, prompted: $prompted, retro: $retro}' > "$OUT/$a.done" ) &
  sleep 2
done
wait

# Let queued pushes (outer loop) finish, then full CI on every pushed branch (ground truth).
for _ in $(seq 1 60); do
  [ -z "$(ls "$REPO"/.agents/worktrees/*/.agents/outer_loop/jobs/*.json 2>/dev/null | xargs grep -l '"status": "\(running\|queued\)"' 2>/dev/null)" ] && break
  sleep 5
done
: > "$OUT/ci.txt"
for br in $(git --git-dir="$WORK/remote.git" for-each-ref --format='%(refname:short)' refs/heads | grep -v '^main$'); do
  d="$WORK/gt-$(echo "$br" | tr / _)"; rm -rf "$d"; git clone -q -b "$br" "$WORK/remote.git" "$d"
  if (cd "$d" && bash ci/run.sh > "$d.log" 2>&1); then echo "$br PASS" >> "$OUT/ci.txt"; else echo "$br FAIL" >> "$OUT/ci.txt"; fi
done

for a in "${AGENTS[@]}"; do
  jq -s --arg a "$a" --argjson done "$(cat "$OUT/$a.done")" --arg since "$since" \
    --argjson filed "$(jq -s --arg a "$a" --arg s "$since" '[.[] | select(.agent == $a and .ts > $s)] | length' "$MONOFLOW_IMPROVE_DIR/suggestions.jsonl" 2>/dev/null || echo 0)" '
    [.[] | select(.type=="step_finish") | .part.tokens] as $t |
    $done + { filed: $filed, tool_calls: ([.[] | select(.type=="tool_use")] | length),
      fresh_tokens: (($t|map(.input)|add // 0) + ($t|map(.output)|add // 0)) }' "$OUT/$a.jsonl"
done > "$OUT/metrics.jsonl"
{
  echo "round since $since · model $MODEL · agents ${#AGENTS[@]}"
  jq -s '{retros: (map(select(.retro))|length), unprompted: (map(select(.retro and .prompted==0))|length),
    suggestions: (map(.filed)|add), avg_secs: (map(.secs)|add/length|floor),
    avg_tool_calls: (map(.tool_calls)|add/length*10|floor/10), avg_fresh_tokens: (map(.fresh_tokens)|add/length|floor)}' "$OUT/metrics.jsonl"
  echo "pushed=$(wc -l < "$OUT/ci.txt" | tr -d ' ') red=$(grep -c FAIL "$OUT/ci.txt")"
  echo
  "$BIN" improve triage --since="$since"
} > "$OUT/round.txt" 2>&1
cat "$OUT/round.txt"
