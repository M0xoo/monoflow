Agent id: agent-money. Work in libs/money.
Change `Percent` to round HALF-UP instead of rounding down (Percent(1005, 10) == 101, Percent(1004, 10) == 100) and update its tests. Other packages depend on libs/money: make sure they all still pass. Commit and push your branch.
