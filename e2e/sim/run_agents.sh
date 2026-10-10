#!/usr/bin/env bash
# Drive concurrent opencode agents against a provisioned sim repo.
# Usage: e2e/sim/run_agents.sh <workdir> <monoflow-binary> <outdir> [model] [agents...]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${1:?workdir}; BIN=${2:?bin}; OUT=${3:?outdir}; MODEL=${4:-opencode/big-pickle}; shift 4 || shift $#
AGENTS=("$@"); [ ${#AGENTS[@]} -eq 0 ] && AGENTS=(agent-billing agent-money agent-ui agent-ledger)
REPO=$(bash "$HERE/setup.sh" "$WORK" "$BIN" | tail -1)
mkdir -p "$OUT"
for a in "${AGENTS[@]}"; do
  prompt="$(cat "$HERE/tasks/_preamble.md" "$HERE/tasks/$a.md")"
  ( start=$(date +%s)
    opencode run --dir "$REPO" -m "$MODEL" --auto --format json --title "$a" "$prompt" > "$OUT/$a.jsonl" 2> "$OUT/$a.stderr"
    echo "{\"agent\":\"$a\",\"rc\":$?,\"secs\":$(( $(date +%s)-start ))}" > "$OUT/$a.done" ) &
  sleep 2
done
wait
# Ground truth: with a real GitHub remote, the GitHub Actions conclusion per branch head;
# otherwise run the full CI script on every branch that reached the local remote.
: > "$OUT/remote_ci.txt"
if [ -n "${SIM_GH_REPO:-}" ]; then
  sleep 5
  gh run list -R "$SIM_GH_REPO" --limit 50 --json headBranch,headSha,status,conclusion,event \
    -q '.[] | "\(.headBranch) \(.event) \(.status) \(.conclusion) \(.headSha[0:8])"' >> "$OUT/remote_ci.txt"
  gh pr list -R "$SIM_GH_REPO" --state all --json number,headRefName,title -q '.[] | "PR #\(.number) \(.headRefName) \(.title)"' >> "$OUT/remote_ci.txt"
fi
for br in $( [ -z "${SIM_GH_REPO:-}" ] && git --git-dir="$WORK/remote.git" for-each-ref --format='%(refname:short)' refs/heads); do
  d="$WORK/ci-$(echo "$br" | tr / _)"; rm -rf "$d"
  git clone -q -b "$br" "$WORK/remote.git" "$d" 2>/dev/null
  if (cd "$d" && bash ci/run.sh >/dev/null 2>"$d.err"); then r=PASS; else r=FAIL; fi
  echo "$br $r $(tail -3 "$d.err" | tr '\n' ' ')" >> "$OUT/remote_ci.txt"
done
( cd "$REPO" && ./bin/monoflow pr list; ./bin/monoflow cache status; ./bin/monoflow events list; git worktree list; git branch --show-current ) > "$OUT/final_state.txt" 2>&1
# How often agents reached for raw tools vs MonoFlow (from the transcripts).
for a in "${AGENTS[@]}"; do
  jq -r 'select(.type=="tool_use" and .part.tool=="bash") | .part.state.input.command' "$OUT/$a.jsonl" 2>/dev/null > "$OUT/$a.commands"
  printf '%s monoflow=%s raw_checks=%s raw_git=%s\n' "$a" \
    "$(grep -c 'monoflow ' "$OUT/$a.commands")" \
    "$(grep -cE '(^|[;&|] *)(go (test|build|vet)|npm (run )?test|node [a-z/]*test\.js|gofmt)' "$OUT/$a.commands")" \
    "$(grep -cE '(^|[;&|] *)git (commit|push|pull|checkout|switch)' "$OUT/$a.commands")"
done > "$OUT/tool_usage.txt"
echo "$OUT"
