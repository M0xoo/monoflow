#!/usr/bin/env bash
# Provision a realistic MonoFlow-governed monorepo for multi-agent simulation.
# Usage: e2e/sim/setup.sh <workdir> <monoflow-binary>
set -euo pipefail
WORK=${1:?workdir}; BIN=${2:?monoflow binary}
rm -rf "$WORK"; mkdir -p "$WORK"
REMOTE="$WORK/remote.git"; REPO="$WORK/acme"
git init -q --bare -b main "$REMOTE"
git init -q -b main "$REPO"; cd "$REPO"
git config user.name "Sim Maintainer"; git config user.email maintainer@acme.test

# --- Go service modules (go.work) ---------------------------------------
mkdir -p libs/money services/billing services/ledger apps/web packages/ui packages/api-client docs
cat > go.work <<'G'
go 1.22

use (
	./libs/money
	./services/billing
	./services/ledger
)
G
cat > libs/money/go.mod <<'G'
module acme/libs/money

go 1.22
G
cat > libs/money/money.go <<'G'
package money

// Cents is an amount in minor units.
type Cents int64

// Add returns a+b.
func Add(a, b Cents) Cents { return a + b }

// Percent returns p percent of c, rounded down.
func Percent(c Cents, p int64) Cents { return Cents(int64(c) * p / 100) }
G
cat > libs/money/money_test.go <<'G'
package money

import "testing"

func TestAdd(t *testing.T) {
	if Add(150, 250) != 400 {
		t.Fatal("Add broken")
	}
}

func TestPercent(t *testing.T) {
	if Percent(1000, 20) != 200 {
		t.Fatal("Percent broken")
	}
}
G
for svc in billing ledger; do
cat > services/$svc/go.mod <<G
module acme/services/$svc

go 1.22

require acme/libs/money v0.0.0

replace acme/libs/money => ../../libs/money
G
done
cat > services/billing/billing.go <<'G'
package billing

import "acme/libs/money"

// Invoice totals line items and applies VAT.
func Invoice(lines []money.Cents, vatPercent int64) money.Cents {
	var sum money.Cents
	for _, l := range lines {
		sum = money.Add(sum, l)
	}
	return money.Add(sum, money.Percent(sum, vatPercent))
}
G
cat > services/billing/billing_test.go <<'G'
package billing

import (
	"testing"

	"acme/libs/money"
)

func TestInvoice(t *testing.T) {
	got := Invoice([]money.Cents{1000, 500}, 20)
	if got != 1800 {
		t.Fatalf("Invoice = %d, want 1800", got)
	}
}
G
cat > services/ledger/ledger.go <<'G'
package ledger

import "acme/libs/money"

// Balance sums postings.
func Balance(postings []money.Cents) money.Cents {
	var b money.Cents
	for _, p := range postings {
		b = money.Add(b, p)
	}
	return b
}
G
cat > services/ledger/ledger_test.go <<'G'
package ledger

import (
	"testing"

	"acme/libs/money"
)

func TestBalance(t *testing.T) {
	if Balance([]money.Cents{100, -40, 15}) != 75 {
		t.Fatal("Balance broken")
	}
}
G

# --- JS workspace -----------------------------------------------------------
cat > package.json <<'J'
{ "name": "acme", "private": true, "workspaces": ["packages/*", "apps/*"] }
J
cat > packages/ui/package.json <<'J'
{ "name": "@acme/ui", "version": "1.0.0", "main": "index.js", "scripts": { "test": "node test.js" } }
J
cat > packages/ui/index.js <<'J'
function formatPrice(cents) { return "$" + (cents / 100).toFixed(2); }
module.exports = { formatPrice };
J
cat > packages/ui/test.js <<'J'
const assert = require("assert");
const { formatPrice } = require("./index.js");
assert.strictEqual(formatPrice(1999), "$19.99");
console.log("ui ok");
J
cat > packages/api-client/package.json <<'J'
{ "name": "@acme/api-client", "version": "1.0.0", "main": "index.js", "scripts": { "test": "node test.js" } }
J
cat > packages/api-client/index.js <<'J'
function invoiceUrl(id) { return "/api/invoices/" + encodeURIComponent(id); }
module.exports = { invoiceUrl };
J
cat > packages/api-client/test.js <<'J'
const assert = require("assert");
const { invoiceUrl } = require("./index.js");
assert.strictEqual(invoiceUrl("a b"), "/api/invoices/a%20b");
console.log("api-client ok");
J
cat > apps/web/package.json <<'J'
{ "name": "@acme/web", "version": "1.0.0", "main": "index.js", "scripts": { "test": "node test.js" },
  "dependencies": { "@acme/ui": "*", "@acme/api-client": "*" } }
J
cat > apps/web/index.js <<'J'
const { formatPrice } = require("../../packages/ui/index.js");
const { invoiceUrl } = require("../../packages/api-client/index.js");
function invoiceRow(inv) { return `<a href="${invoiceUrl(inv.id)}">${formatPrice(inv.total)}</a>`; }
module.exports = { invoiceRow };
J
cat > apps/web/test.js <<'J'
const assert = require("assert");
const { invoiceRow } = require("./index.js");
assert.strictEqual(invoiceRow({ id: "7", total: 1800 }), '<a href="/api/invoices/7">$18.00</a>');
console.log("web ok");
J
echo "# Acme docs" > docs/README.md

# --- Remote CI (mirrors what GitHub Actions would run) -----------------------
mkdir -p ci
cat > ci/run.sh <<'C'
#!/usr/bin/env bash
# Full CI: every Go module and every JS package test.
set -e
for m in libs/money services/billing services/ledger; do (cd "$m" && go vet ./... && go test ./...); done
for p in packages/ui packages/api-client apps/web; do (cd "$p" && node test.js); done
C
chmod +x ci/run.sh

git add -A && git commit -qm "chore: initial acme monorepo"
"$BIN" init . >/dev/null
git add -A && MONOFLOW_INTERNAL=1 git commit -qm "chore: install monoflow" --no-verify
git remote add origin "$REMOTE"
MONOFLOW_INTERNAL=1 git push -q origin main
mkdir -p bin && ln -sf "$BIN" bin/monoflow
echo "$REPO"
