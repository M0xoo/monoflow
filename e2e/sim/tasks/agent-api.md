Agent id: agent-api. Work in packages/api-client (JavaScript workspace package "@acme/api-client").
The API team renamed the endpoint: rename `invoiceUrl(id)` to `invoicePath(id)` and make it return "/api/v2/invoices/<id>" (still URL-encoded). Remove the old name.
Every consumer of @acme/api-client in the monorepo must keep working in the same PR. Use monoflow to find them and verify them.
Commit with monoflow, open a PR with monoflow, push it, then check your event inbox once.
