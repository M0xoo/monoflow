Agent id: agent-ledger-upgrade. Work in services/ledger.
Add `func Net(credits, debits []money.Cents) money.Cents` returning sum(credits) - sum(debits), implemented with money.Sub, plus a test.
services/ledger currently uses a pinned release of libs/money that may not have Sub yet. Use monoflow to find out and to upgrade it properly.
Commit with monoflow and open a PR with monoflow, then check your event inbox once.
