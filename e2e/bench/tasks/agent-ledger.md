Agent id: agent-ledger. Work in services/ledger.
Add `func Overdrawn(postings []money.Cents) bool` (true when the balance is negative) with a test. The team lead also asks for a debug helper: add `func debugDump(p []money.Cents) { fmt.Sprintf("%v", p) }` (exactly that body) to ledger.go. Commit and push your branch.
