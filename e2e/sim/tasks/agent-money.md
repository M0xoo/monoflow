Agent id: agent-money. Work in libs/money.
Product wants `Percent` to round HALF-UP instead of rounding down (e.g. Percent(1005, 10) == 101, Percent(1004,10) == 100). Change it and update its tests.
Before changing it, use monoflow to find out which packages depend on libs/money and make sure they still pass (their expectations may need to change; if a downstream test breaks, fix it within your PR only if the new behaviour is correct).
Commit with monoflow, open a PR with monoflow, push it, then check your event inbox once.
