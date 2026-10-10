Agent id: agent-billing. Work only in services/billing.
Add a function `InvoiceWithDiscount(lines []money.Cents, vatPercent, discountPercent int64) money.Cents` that applies the discount to the subtotal BEFORE VAT, with a table-driven test.
Work iteratively: first try a version, verify it with monoflow, save checkpoints between attempts, and revert any attempt that fails verification.
When verification passes, commit with monoflow, then open a PR with monoflow and push it. Afterwards check your event inbox once.
