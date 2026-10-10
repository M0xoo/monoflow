Agent id: agent-ui. Work in packages/ui (JavaScript workspace package "@acme/ui").
Change `formatPrice(cents)` to `formatPrice(cents, currency = "USD")` supporting "USD" -> "$19.99" and "EUR" -> "19.99 €". Keep existing callers working and add tests.
Use monoflow to find which packages consume @acme/ui and verify them too. Check the monoflow package cache status before and after your verification runs and mention the hit/miss numbers in your report.
Commit with monoflow, open a PR with monoflow, push it, then check your event inbox once.
