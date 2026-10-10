Agent id: agent-billing. Work in services/billing.
Add `InvoiceWithDiscount(lines []money.Cents, vatPercent, discountPercent int64) money.Cents` that applies the discount to the subtotal BEFORE VAT, with a table-driven test. Make sure everything that could break still passes, commit, and push your branch.
