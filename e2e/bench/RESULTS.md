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

### Cost to green CI (run 6)
A red branch is not finished work. Since run 6 the harness hands every branch that fails full CI back to
the same agent session with the CI output (`FIX_ROUNDS`, default 2), so metrics measure the cost to green:

| run 6 | secs | tool calls | fresh tokens | fix rounds | red at the end |
|---|---|---|---|---|---|
| A (MonoFlow) | 102 | 19.1 | 138k | 0 | 0 / 8 |
| B (plain git) | 107 | 19.2 | 147k | 2 | 0 / 8 |

Runs 6-8 combined (16 agents per condition; runs 9-10 were aborted when the free model was rate-limited):

| runs 6-8 | avg / median secs | tool calls | avg / median fresh tokens | red pushes (fix rounds) | red at the end |
|---|---|---|---|---|---|
| A (MonoFlow) | 105 / 109 | 19.1 | 140k / 149k | **0** | 0 / 16 |
| B (plain git) | 110 / 102 | 19.1 | 140k / 133k | **4** | 0 / 16 |

B's seconds exclude the remote CI run that reported the failure (minutes on real CI; here `ci/run.sh`
runs locally). Small sample (8 agents per condition): read it as "on par to green, without the red push",
not as a precise speed-up. With 16 agents per condition the cost to green is the same; the
difference is 4 red pushes (4 wasted remote CI runs, each found minutes later in real CI) versus none.

## Reading
- **Remote CI protection works**: 0 of 24 MonoFlow branches failed full CI; 6 of 24 plain branches did
  (the same `agent-ledger` cross-package break every run — B agents only tested their own package).
- **Cost to push** (runs 3-5): MonoFlow agents spend ~20-35% more tokens and ~5-7 more tool calls on the
  verification B skips. **Cost to green** (run 6, B fixes its red branches): on par or slightly lower, with
  no red push and no extra remote CI run.
- Run-to-run noise (B: 84-128 s) is larger than the effect of the output/workflow tweaks, so 4 and 5
  are not distinguishable. Measured directly instead: `pr create` output 6.0 KB -> 0.5 KB,
  `verify` 1.1 KB -> 0.56 KB, `outer wait` 1.6 KB -> 0.2 KB; redundant `push` calls 8 -> 0.

## Bugs the benchmark found (fixed)
outer loop false red from sparse worktrees; headless opencode idle hang; `push` blocked every
branch with commits; worktrees cut from a stale local main; `events wait` answering with a pre-PR
CI_SKIPPED; `pr create --body-file` silently ignored; local verify skipping `cmd/` tests.

## CI proof reuse on real Linux (2026-10-11)
Sandbox PR M0xoo/monoflow-sim-acme#26: a `proof-agent` workflow on ubuntu-latest plays the agent
(outer loop on the exact commit, then `monoflow ci proofs publish`), then the PR's `monoflow-ci`
run imports them.

| PR CI run | proofs | targets cached | verify step |
|---|---|---|---|
| no proofs for the changed packages | – | 4 / 6 (unchanged packages, actions/cache) | 21.6 s |
| proofs published by the agent | imported 6 records | **6 / 6** | **0.03 s** |

Found on the way: the vendored `.agents/bin/monoflow-linux-amd64` from the sim setup was older
than `ci proofs` and silently ran a different command (keep the vendored binary current, or let
CI download the release); proofs were published after the branch push, racing an open PR's CI
(now published first).

## Large repo: opentelemetry-go (29 Go modules), `e2e/bench/bigrepo.sh`
Task: rename `attribute.StringValue` -> `StringVal` (root module) and fix every caller across
nested modules. One agent per condition, `opencode/big-pickle`. Ground truth: `go vet ./...` in every
module, ignoring modules already failing on main (the root module, upstream vet debt).

Verification without an agent (partial rename, nested callers left broken): root-only
`go build && go test ./...` passes in 12 s and misses all 22 broken modules; `monoflow verify --affected`
finds them in 14 s.

| build | A secs | B secs | A fresh tokens | B fresh tokens | green |
|---|---|---|---|---|---|
| before fixes (3 runs) | 382 | 208 | 102k | 72k | 3/3 vs 3/3 |
| + cache keys, root projection, API-diff perf (5 runs) | **328** | 368 | 103k | 82k | 5/5 vs 5/5 |
| + compact agent outputs, shorter AGENTS.md (3 runs, `step-5-preview-free`*) | **768** | 1120 | **65k** | 80k | 3/3 vs 3/3 |

\* `big-pickle` was rate-limited for the whole rerun (every agent timed out with no output), so this
row uses another free model. Compare A to B within a row, not across rows.

Fixed along the way (each was a MonoFlow cost on big repos): a root package projected only its
top-level files into a worktree (~15 wasted agent calls); cache keys of a root package hashed every
nested module, docs and ignored local files, so the outer loop re-ran 27 modules after `verify`
(0 -> 26/27 cached, 46 s -> 8 s); the API diff in `pr create` parsed all 29 modules with one git
process per file (15 s -> 1.2 s; `pr create` 38 s -> 7-9 s); verify printed the same compiler error
once per module (25 KB -> 7 KB).

The token gap came from MonoFlow's own outputs: `pr status` returned 12 KB of JSON, `events unread`
returned full event payloads, and AGENTS.md was 5.1 KB. Making these compact by default (`--verbose`
restores the full output) and trimming AGENTS.md to 3.2 KB reversed the gap.

Reading: on a large repo MonoFlow is now ahead on both time (-31%) and fresh tokens (-19%), with the
same green rate, and it catches the cross-module breakage that root-only checks miss. Variance is
high (460-1312 s per run), so treat the deltas as directional.

## Live: idle opencode session woken by an event
`opencode serve` on a fixture repo after `monoflow init` (installs `.opencode/plugin/monoflow.js`). The
agent was told to edit, `monoflow commit`, and end its turn. The change passed `verify --affected`
but read a gitignored file, so the outer loop (clean checkout of the exact commit) failed.
With no command run by anyone, the plugin's `session.idle` handler waited for the outer loop and
prompted the session with `[MonoFlow events] OUTER_LOOP_REGRESSION ...`. The agent found the cause,
committed the fixture, re-verified, re-ran `outer start`, and the outer loop went green. Push stayed
blocked until then. A green outer loop wakes nothing (no action needed), as intended.

Found: the test failure was reported as `.:1:` (unparsed), so the agent spent 6 calls reading raw
logs. Go test assertions (`--- FAIL: TestX` / `x_test.go:11: msg`) are now parsed into
`file:line` diagnostics with the test name.
