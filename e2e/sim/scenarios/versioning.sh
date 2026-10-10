#!/usr/bin/env bash
# Versioning scenario: libs/money released at 1.0.0 and 1.1.0 (adds Sub);
# services/ledger pinned to 1.0.0, services/billing on the live code.
set -euo pipefail
cd "${1:?repo}"
B=./bin/monoflow
$B version release libs/money >/dev/null
$B version pin services/ledger libs/money >/dev/null
git add -A && MONOFLOW_INTERNAL=1 git commit -qm "chore(ledger): pin libs/money@1.0.0" --no-verify
cat >> libs/money/money.go <<'G'

// Sub returns a-b.
func Sub(a, b Cents) Cents { return a - b }
G
$B version bump libs/money minor >/dev/null
git add -A && MONOFLOW_INTERNAL=1 git commit -qm "feat(money): add Sub" --no-verify
$B version release libs/money >/dev/null
MONOFLOW_INTERNAL=1 git push -q origin main --tags
echo "versioning scenario ready: $(git tag | tr '\n' ' ')"
