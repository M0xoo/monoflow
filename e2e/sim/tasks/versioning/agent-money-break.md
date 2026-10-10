Agent id: agent-money-break. Work in libs/money.
Product wants explicit rounding: change `Percent(c Cents, p int64) Cents` to `Percent(c Cents, p int64, mode Rounding) Cents` with `type Rounding int` and constants `RoundDown` (current behaviour) and `RoundHalfUp`. Add tests.
This is a breaking API change. Use monoflow to understand which packages consume libs/money and how (live code vs pinned release), keep every consumer that builds against the live code working in the same PR, and record the right version bump for libs/money (monoflow can tell you which).
Commit with monoflow and open a PR with monoflow, then check your event inbox once.
