# A/B benchmark results (2026-10-11)

Same 4 concurrent tasks on the `acme` sim repo (Go modules + JS workspace, cross-package
dependencies), 2 runs per condition, `opencode/nemotron-3-ultra-free`.
A = MonoFlow (worktrees, verify, outer loop, pr create). B = plain git clones + the repo's own tools.
Ground truth: `ci/run.sh` (full CI) on every pushed branch.

| run | build | A secs | B secs | A tool calls | B tool calls | A fresh tokens | B fresh tokens | A red / pushed | B red / pushed |
|---|---|---|---|---|---|---|---|---|---|
| 3 | push-gate fix | 153 | 96 | 24.1 | 18.6 | 177k | 135k | **0 / 8** | 2 / 8 |
| 4 | + compact output | 119 | 84 | 22.3 | 17.1 | 151k | 125k | **0 / 8** | 2 / 8 |
| 5 | + "pr create right after commit" | 176 | 128 | 24.8 | 17.8 | 182k | 134k | **0 / 8** | 2 / 8 |

(Averages per agent. Runs 1-2 were invalid: the sparse outer-loop false regression, the
headless opencode hang, a rate-limited model, and the push gate bug — all fixed.)

## Reading
- **Remote CI protection works**: 0 of 24 MonoFlow branches failed full CI; 6 of 24 plain branches did
  (the same `agent-ledger` cross-package break every run — B agents only tested their own package).
- **Cost**: MonoFlow agents spend ~20-35% more tokens and ~5-7 more tool calls. Those calls are the
  verification and gating B skips; B's numbers exclude the second cycle a red branch costs (CI run,
  failure triage, fix, push).
- Run-to-run noise (B: 84-128 s) is larger than the effect of the output/workflow tweaks, so 4 and 5
  are not distinguishable. Measured directly instead: `pr create` output 6.0 KB -> 0.5 KB,
  `verify` 1.1 KB -> 0.56 KB, `outer wait` 1.6 KB -> 0.2 KB; redundant `push` calls 8 -> 0.

## Bugs the benchmark found (fixed)
outer loop false red from sparse worktrees; headless opencode idle hang; `push` blocked every
branch with commits; worktrees cut from a stale local main; `events wait` answering with a pre-PR
CI_SKIPPED; `pr create --body-file` silently ignored; local verify skipping `cmd/` tests.
