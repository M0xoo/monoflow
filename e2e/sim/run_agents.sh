#!/usr/bin/env bash
# Drive concurrent opencode agents against a provisioned sim repo.
# Usage: e2e/sim/run_agents.sh <workdir> <monoflow-binary> <outdir> [model] [agents...]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WORK=${1:?workdir}; BIN=${2:?bin}; OUT=${3:?outdir}; MODEL=${4:-opencode/big-pickle}; shift 4 || shift $#
AGENTS=("$@"); [ ${#AGENTS[@]} -eq 0 ] && AGENTS=(agent-billing agent-money agent-ui agent-ledger agent-api agent-money-sub)
if [ -n "${SIM_GH_REPO:-}" ] && [ -z "${SIM_LINUX_BIN:-}" ] && [ -n "${MONOFLOW_SRC:-}" ]; then
  export SIM_LINUX_BIN="$WORK.monoflow-linux-amd64"
  (cd "$MONOFLOW_SRC" && GOOS=linux GOARCH=amd64 go build -o "$SIM_LINUX_BIN" ./cmd/monoflow)
fi
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
# Ground truth: run the full CI script (ci/run.sh) locally on every branch head that
# reached the remote. Remote CI is MonoFlow's economical workflow, measured separately.
: > "$OUT/remote_ci.txt"
REMOTE_URL=$(git -C "$REPO" remote get-url origin)
for br in $(git ls-remote --heads "$REMOTE_URL" | awk '{print $2}' | sed 's|refs/heads/||'); do
  d="$WORK/ci-$(echo "$br" | tr / _)"; rm -rf "$d"
  git clone -q -b "$br" "$REMOTE_URL" "$d" 2>/dev/null
  if (cd "$d" && bash ci/run.sh >/dev/null 2>"$d.err"); then r=PASS; else r=FAIL; fi
  echo "$br $(git -C "$d" rev-parse --short HEAD) $r $(tail -2 "$d.err" | tr '\n' ' ')" >> "$OUT/remote_ci.txt"
done

# CI economy: remote runs per branch/event; any run on a commit that fails ground truth
# means MonoFlow let a red commit reach CI.
if [ -n "${SIM_GH_REPO:-}" ]; then
  for _ in $(seq 1 60); do
    pending=$(gh run list -R "$SIM_GH_REPO" --limit 100 --json status -q '[.[] | select(.status != "completed")] | length')
    [ "$pending" = "0" ] && break
    sleep 10
  done
  gh run list -R "$SIM_GH_REPO" --limit 100 --json headBranch,headSha,event,status,conclusion \
    -q '.[] | "\(.headBranch) \(.headSha[0:7]) \(.event) \(.conclusion)"' > "$OUT/ci_runs.txt"
  {
    echo "total_runs=$(wc -l < "$OUT/ci_runs.txt" | tr -d ' ')"
    echo "by_event: $(awk '{print $3}' "$OUT/ci_runs.txt" | sort | uniq -c | tr '\n' ' ')"
    echo "by_conclusion: $(awk '{print $4}' "$OUT/ci_runs.txt" | sort | uniq -c | tr '\n' ' ')"
    echo "runs_per_branch: $(awk '{print $1}' "$OUT/ci_runs.txt" | sort | uniq -c | tr '\n' ' ')"
    echo "prs=$(gh pr list -R "$SIM_GH_REPO" --state all --json number -q length)"
  } > "$OUT/ci_economy.txt"
  sleep 20 # let detached MonoFlow watchers record CI events
fi

# Worktree footprint vs the full repo (files checked out, KB on disk excluding .git).
{
  printf 'root files=%s kb=%s\n' "$(git -C "$REPO" ls-files | wc -l | tr -d ' ')" "$(du -sk --exclude=.git "$REPO" 2>/dev/null | cut -f1 || du -sk "$REPO" | cut -f1)"
  for wt in "$REPO"/.agents/worktrees/*/; do
    [ -d "$wt" ] || continue
    printf '%s files=%s kb=%s\n' "$(basename "$wt")" "$(cd "$wt" && find . -path ./.git -prune -o -path ./.agents -prune -o -type f -print | wc -l | tr -d ' ')" "$(du -sk "$wt" | cut -f1)"
  done
} > "$OUT/worktree_footprint.txt"

( cd "$REPO" && ./bin/monoflow worktree list; ./bin/monoflow cache status; ./bin/monoflow events list; git branch --show-current ) > "$OUT/final_state.txt" 2>&1
# How often agents reached for raw tools vs MonoFlow (from the transcripts).
for a in "${AGENTS[@]}"; do
  jq -r 'select(.type=="tool_use" and .part.tool=="bash") | .part.state.input.command' "$OUT/$a.jsonl" 2>/dev/null > "$OUT/$a.commands"
  printf '%s monoflow=%s raw_checks=%s raw_git=%s\n' "$a" \
    "$(grep -c 'monoflow ' "$OUT/$a.commands")" \
    "$(grep -cE '(^|[;&|] *)(go (test|build|vet)|npm (run )?test|node [a-z/]*test\.js|gofmt)' "$OUT/$a.commands")" \
    "$(grep -cE '(^|[;&|] *)git (commit|push|pull|checkout|switch)' "$OUT/$a.commands")"
done > "$OUT/tool_usage.txt"
echo "$OUT"
