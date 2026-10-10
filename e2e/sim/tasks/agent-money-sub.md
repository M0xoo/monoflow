Agent id: agent-money-sub. Work in libs/money and services/ledger.
Add `func Sub(a, b Cents) Cents` to libs/money (with a test), then change services/ledger's Balance to keep using money.Add (no behaviour change) and add `func Net(credits, debits []money.Cents) money.Cents` that uses money.Sub (with a test).
Another agent may be changing libs/money/money.go at the same time; make sure your branch is up to date with main before you open your PR (use monoflow, not raw git).
Commit with monoflow, open a PR with monoflow, push it, then check your event inbox once.
