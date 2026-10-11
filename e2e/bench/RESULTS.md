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
| + compact agent outputs, shorter AGENTS.md (6 runs, `step-5-preview-free`*) | **908** (median 811) | 1024 (median 1025) | **70k** (median 74k) | 76k (median 75k) | 6/6 vs 6/6 |

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

Reading: the first 3 runs showed MonoFlow far ahead (768 s / 65k vs 1120 s / 80k); 3 more runs on
the same build shrank that to about -11% time and -8% tokens on average, and near parity on median
tokens. So on a large repo MonoFlow no longer costs extra time or context, with the same green rate,
and it catches the cross-module breakage that root-only checks miss. Per-run variance (460-1638 s)
is larger than the difference, so this is parity-or-better, not a proven win.

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

Also found: with the cache on, the outer loop first passed in ~100 ms, reusing the inner-loop result,
because gitignored files were not in the cache key. Loose gitignored files in a package (not ignored
directories, symlinks or OS junk) are now part of the key, so a clean checkout re-runs instead of
inheriting a result that may depend on a local-only file. Re-run with the cache on: outer loop
regressed as it should.

## opentelemetry-go: a change caught only by another module's tests (`tasks/status-codes.txt`)
Task: upper-case `codes.Code` names (`UNSET/ERROR/OK`). Besides `codes`' own tests, the change breaks
`exporters/stdout/stdouttrace` (a JSON expectation), which root-only checks miss. Ground truth:
`go test ./...` in every module. 3 runs per side, `step-5-preview-free`, build before the fixes below.

| | MonoFlow | plain git |
|---|---|---|
| green branches | 2/3 | 2/3 |
| runs hitting the 30 min limit | 2 | 1 |
| avg tool calls | 86 | 51 |
| avg fresh tokens | 75k | 51k |

MonoFlow lost this one. The plain agents also found the stdouttrace failure (they ran `go test` in
every module), so the task did not separate the two as hoped, and MonoFlow's own friction cost the
agents time. From the transcripts:
- `verify --affected` said green while `verify --all` said red for the same preexisting gofmt debt
  (only `--affected` classified preexisting failures): the agent stopped trusting both and wrote its
  own test-every-module script. Fixed: every verify mode shares one classified path.
- `graph query codes` / `verify --package=codes` returned "not found" (`codes` is a Go package inside
  the root module). Fixed: both accept a path inside a package.
- Before this run: a preexisting lint failure (otel's unformatted semconv files) stopped the check
  chain, so the root module's tests never ran and were reported green. Fixed: checks after a
  preexisting failure still run.

## MonoFlow's worst case, rerun with Claude Haiku (opentelemetry-go, `tasks/status-codes.txt`)
Same task as above, where MonoFlow had lost (75k vs 51k tokens). Agent: Claude Haiku as a Claude Code
subagent, one run at a time, fresh clone each run; both arms told to read AGENTS.md; ground truth
`go test ./...` in every module of the pushed branch. All 19 runs pushed a green branch.

| | runs | time avg (median) | tool calls avg (median) | tokens avg (median) |
|---|---|---|---|---|
| plain git | 4 | 177 s (130 s) | 24 (14.5) | 60.4k (55k) |
| MonoFlow, first build (A-1..A-3) | 3 | 183 s | 20 | 65k |
| MonoFlow, current build (A-8..A-11) | 4 | **101 s (101 s)** | **15 (14.5)** | 59.9k (59.9k) |

Plain-git runs: 114 / 109 / 338 / 146 s, 15 / 13 / 14 / 54 calls, 53 / 51 / 57 / 81k tokens.
MonoFlow runs: 112 / 110 / 90 / 93 s, 14 / 17 / 15 / 14 calls, 60 / 61 / 59 / 59k tokens.

Reading: MonoFlow is now faster (median -22%, mean -43%) and far more predictable (90-112 s vs
109-338 s; 14-17 calls vs 13-54). Tokens: mean equal, median ~9% higher (the MonoFlow guide and
its outputs in context); a plain run that loses its way costs far more (81k). Every MonoFlow push
was gated on local CI on the exact commit; a plain push is not.

What got it there (each from a transcript, in order):
- one verify path for every mode: --affected, --all and --package used to disagree on old gofmt debt
- verify states its scope ("27 of 29 packages... the other 2: internal/tools, schema, cannot be
  affected") and the next step; preexisting failures spelled out; agents stopped re-testing
- Go package-level affected testing: only packages whose code or tests import the change, gofmt on
  changed files only (CHANGELOG/README edits do not disable it); 28 s vs 58 s for an agent's loop
- `graph query <dir>`: every package in every module importing it, in 0.9 s, instead of grep
- upstream cache keys memoised per run: cached outer loop 14.7 s -> 7.8 s
- `ship` returns at once (push + PR once local CI is green, SHIP_FAILED otherwise); on main it
  moves the work to its own branch and worktree itself
- compact help (0.6 KB) and agent guide (1.4 KB, first in AGENTS.md)

## trpc (pnpm + Turborepo, 11 packages), Claude Haiku (`tasks/trpc-validation-422.txt`)
Input validation errors become 422 instead of 400: one line in `packages/server`, then tests,
inline snapshots and docs across server, client, openapi and adapters. Same setup as the Haiku
otel runs (one run at a time, fresh copy each). The plain arm has the repo's own Turborepo.

| | build | time (s) | tool calls | tokens | ground truth |
|---|---|---|---|---|---|
| plain git + Turbo B-1..B-5 | | 345 / 237 / 239 / 307 / 349 (mean 295) | 32 / 36 / 29 / 36 / 32 | 79k / 87k / 83k / 91k / 93k (mean 87k) | pass (5/5) |
| MonoFlow A-1..A-4 | early | 539 / 586 / 470 / 377 | 24 / 41 / 28 / 34 | 76k / 106k / 83k / 91k | pass |
| MonoFlow A-5, A-6 | v20 | 610* / 398 | 23 / 27 | 87k / 77k | A-6 never pushed (bug below) |
| MonoFlow A-7 | v22 | 839 | 35 | 82k | correct; push blocked by a flaky test (fixed) |
| MonoFlow A-8 | v23 | 408 | 33 | 83k | pass |
| MonoFlow A-9 | v24 | 339 | 40 | 95k | pass |
| MonoFlow A-10 | v25 | 423 | 27 | 89k | pass |
| MonoFlow A-11 | v26 | 411 | 29 | 77k | correct; push blocked by a flaky test under my load** |
| MonoFlow A-12 | v27 | 292 | 26 | 100k | pass |
| MonoFlow A-13 | v29 | **277** | **22** | 87k | pass*** |
| MonoFlow A-14 | v30 | 377 | 22 | 87k | pass (skipped start: no background verify) |
| MonoFlow A-15 | v31 | 307 | 33 | 90k | pass*** |
| MonoFlow A-16 | v31 | 312 | 25 | 90k | pass |
| MonoFlow A-17 | v31 | 433 | 24 | 100k | correct, but its push was wrongly blocked (fixed in v32, below) |

\* A-5's time is contaminated (heavy runs of mine on the same machine during it).
\*\* My Go test suite ran during A-11's local CI; the failing timing test passes 3/3 alone.
\*\*\* One openapi CLI test timed out in the ground-truth run (5 s limit; it passes alone, the
same file's other CLI tests time out on main too, and the change does not touch the CLI).

**Five runs per side (A-13..A-17 against B-1..B-5): MonoFlow loses on trpc.** Time 341 s
against 295 s (+16%), tokens 91k against 87k (+5%), tool calls 25 against 33 (-24%). Correctness:
every change on both sides was right; MonoFlow wrongly refused one push (A-17). What remains is
the length of each verify (90-145 s on trpc: everything CI runs for the change, 9-11 packages),
where the plain agent runs a few vitest files it picks itself.

Bugs found by A-14..A-17 (fixed):
- agents that skip start got no background verify -> verify outside a worktree starts the watcher
  there (read-only, 15 min idle limit).
- background runs were cancelled by each new edit, so they never finished -> they run to the end,
  then the newest state runs.
- A-17: the test-file re-run counted a pre-existing failure (a memory test failing on main) and a
  flaky one (a ping timing test, passes 5/5 alone) as one result, so the flaky one was judged new
  and blocked a correct push -> each failure is judged on its own.
- the ground-truth script counted openapi CLI test timeouts under its own full parallel run as
  agent errors (A-13, A-15) -> failures are re-run alone before they count, for both arms.

Earlier reading: MonoFlow started far behind here (539-610 s against 237-345 s) and is now level on time
with plain git plus Turbo (277 s against a mean of 274 s, slower than its two fastest runs) with
the fewest tool calls of any run (22 against 29-36), and tokens at the top of the plain range
(87k against 79-87k). Every MonoFlow push was gated on local CI on the exact commit and checked
for conflicts with main; plain pushes were not. What closed the gap, run by run:
- A-7: an agent backgrounded verify and looped 9 minutes on a log line -> verify on unchanged
  files reprints the stored result; a second verify waits for the one running.
- A-7, A-8: a websocket timing test blocked correct changes -> local CI retries a failed package
  once; verify re-runs only the failed test files once (seconds) and reports a pass as flaky.
- A-8: the pre-existing note sent the agent to --verbose (pages of another package's lint debt)
  -> pre-existing failures listed inline.
- A-9: the agent ran prettier itself -> per-package fix_cmd formats changed files in verify/ship.
- A-10: a --update-snapshots pass was not reused by the next plain verify (60 s) -> stored under
  the plain key too; failures compared against main by running only the failed test files there.
- A-11: a test edit in @trpc/server re-ran its 8 dependents (66 s) -> an upstream's tests no
  longer count in its dependents' cache keys; the main baseline is prepared in the background.
- A-12: 3-5 verify rounds, one only to discover the dependents' failures -> dependents run in the
  same verify when the changed packages fail only on test assertions. A-13: 2 rounds.

What is left: each verify is still 70-110 s on trpc (11 packages; @trpc/openapi's CLI tests alone
take ~50 s), where the plain agent runs a few vitest files. And 3 plain runs is a small sample.

Bugs found by these runs (fixed):
- Worktree tests failed under Vite's `fs.allow` (node_modules symlinked into the main checkout) and
  the baseline hid it as "preexisting": worktrees and CI checkouts now get a real offline
  `pnpm install`.
- Playwright `test-results/` was committed by ship: checks now remove files they create.
- `ship` committed `.agents/deps-installing`, so every local-CI checkout of that commit waited
  10 minutes for an install that was not running (the 931 s outer loop; A-6 never pushed). The
  marker now holds the installer's pid and is never committed.

Not yet measured: the TanStack Query (Nx) comparison.
