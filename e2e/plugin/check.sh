#!/usr/bin/env bash
# Plug MonoFlow into existing public monorepos and measure how well it understands them.
# Usage: e2e/plugin/check.sh <workdir> <monoflow-binary> [name=url@probe-file ...]
# probe-file: a file whose one-line edit should affect its package and its dependents.
set -uo pipefail
WORK=${1:?workdir}; BIN=${2:?monoflow binary}; shift 2
REPOS=("$@")
[ ${#REPOS[@]} -eq 0 ] && REPOS=(
  "changesets=https://github.com/changesets/changesets@packages/types/src/index.ts"
  "otel-go=https://github.com/open-telemetry/opentelemetry-go@attribute/key.go"
  "tokio=https://github.com/tokio-rs/tokio@tokio-macros/src/lib.rs"
  "langchain=https://github.com/langchain-ai/langchain@libs/core/langchain_core/__init__.py"
)
mkdir -p "$WORK"
ms() { python3 -c 'import time;print(int(time.time()*1000))'; }
for spec in "${REPOS[@]}"; do
  name=${spec%%=*}; rest=${spec#*=}; url=${rest%@*}; probe=${rest##*@}
  dir="$WORK/$name"; out="$WORK/$name.report"
  [ -d "$dir/.git" ] || git clone -q --depth 1 "$url" "$dir"
  ( cd "$dir"
    git checkout -q -- . ; git clean -qfdx -e node_modules >/dev/null 2>&1
    echo "== $name ($url)"
    echo "files_in_repo=$(git ls-files | wc -l | tr -d ' ')"
    t0=$(ms); "$BIN" init . > "$WORK/$name.init.json" 2>&1; rc=$?; t1=$(ms)
    echo "init_rc=$rc init_ms=$((t1-t0))"
    jq -r '"packages=\(.packages|length) languages=\([.packages[].language]|group_by(.)|map("\(.[0])=\(length)")|join(","))"' .agents/monoflow.config.json 2>/dev/null
    jq -r '"edges=\([.packages[].dependencies // [] | length]|add) pkgs_without_test_cmd=\([.packages[]|select((.test_cmd//"")=="")]|length)"' .agents/monoflow.config.json 2>/dev/null
    echo "dirty_after_init: $(git status --short | wc -l | tr -d ' ') files ($(git status --short | awk '{print $2}' | head -6 | tr '\n' ' '))"
    echo "probe=$probe"
    t0=$(ms); "$BIN" graph impact "$probe" > "$WORK/$name.impact.json" 2>&1; t1=$(ms)
    jq -c '{owning_package, direct: (.direct_dependents|length), transitive: (.transitive_dependents|length)}' "$WORK/$name.impact.json" 2>/dev/null || head -c 300 "$WORK/$name.impact.json"
    echo "impact_ms=$((t1-t0))"
    echo "// monoflow plugin probe" >> "$probe"
    t0=$(ms); "$BIN" graph affected > "$WORK/$name.affected.json" 2>&1; t1=$(ms)
    jq -c '{directly_changed, all: (.all_affected|length)}' "$WORK/$name.affected.json" 2>/dev/null || head -c 300 "$WORK/$name.affected.json"
    echo "affected_ms=$((t1-t0))"
    git checkout -q -- "$probe"
    owner=$(jq -r '.owning_package // empty' "$WORK/$name.impact.json" 2>/dev/null)
    if [ -n "$owner" ]; then
      t0=$(ms); "$BIN" worktree create plugin-probe --packages="$owner" > "$WORK/$name.wt.json" 2>&1; t1=$(ms)
      wt="$dir/.agents/worktrees/plugin-probe"
      echo "worktree_ms=$((t1-t0)) worktree_files=$(cd "$wt" 2>/dev/null && find . -path ./.git -prune -o -path ./.agents -prune -o -type f -print | wc -l | tr -d ' ') projected=$(jq -c '.packages|length' "$WORK/$name.wt.json" 2>/dev/null)"
      "$BIN" worktree finish plugin-probe --force >/dev/null 2>&1
    fi
  ) > "$out" 2>&1
  cat "$out"
done
