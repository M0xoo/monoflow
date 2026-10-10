#!/usr/bin/env bash
# Condense an opencode --format json transcript into a readable interaction log.
# Usage: e2e/sim/condense.sh <agent.jsonl> [max-output-chars]
MAX=${2:-600}
jq -r --argjson max "$MAX" '
  def trunc: if length > $max then .[0:$max] + " …[+" + ((length - $max)|tostring) + "]" else . end;
  if .type=="tool_use" then
    (.part.tool) as $t | (.part.state) as $s |
    if $t=="bash" then "$ " + ($s.input.command // "") + "\n" + (($s.output // "")|tostring|trunc)
    elif $t=="read" then "[read] " + ($s.input.path // $s.input.filePath // "")
    elif ($t=="write" or $t=="edit") then "[" + $t + "] " + ($s.input.filePath // $s.input.path // "") + (if $s.status=="error" then " ERROR: " + (($s.error // $s.output // "")|tostring|trunc) else "" end)
    else "[" + $t + "] " + (($s.input|tostring)|.[0:200]) end
  elif .type=="text" then "» " + (.part.text // "")
  elif .type=="error" then "!! ERROR " + (tostring|.[0:400])
  else empty end' "$1"
