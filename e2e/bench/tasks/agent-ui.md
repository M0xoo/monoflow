Agent id: agent-ui. Work in packages/ui (JS package "@acme/ui").
Change `formatPrice(cents)` to `formatPrice(cents, currency = "USD")` supporting "USD" -> "$19.99" and "EUR" -> "19.99 €", keeping existing callers working, with tests. Make sure every consumer of @acme/ui still passes. Commit and push your branch.
