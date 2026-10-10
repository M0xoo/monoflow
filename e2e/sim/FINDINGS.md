# Multi-agent simulation findings

Agents: opencode (`opencode/big-pickle`) driven by `e2e/sim/run_agents.sh`, four concurrent
agents on the `e2e/sim/setup.sh` monorepo (Go `go.work` modules + JS workspace, local bare remote).
Ground truth = `ci/run.sh` (full `go vet` + `go test` + JS tests) run on every pushed branch.

## Round 1 (2026-10-10)

Remote CI: 4/5 branches PASS, **`feat/pr-…205` (agent-ledger) FAIL**: `ledger.go:24:2: result of fmt.Sprintf call not used`.
MonoFlow verified, committed, PR'd and pushed it as "passed".

| # | Finding | Evidence |
|---|---------|----------|
| F1 | `verify --affected` runs only `typecheck_cmd` (`go build`); tests and lint never run, `--all-checks` is unimplemented. DoD receipt still claims "Typecheck & Unit Tests passed". | ledger vet failure reached remote; billing shipped a logically wrong `InvoiceWithDiscount` (agent caught it manually: `936, want 935`) |
| F2 | `push` CI watcher is a goroutine in a process that exits immediately: no CI event is ever emitted. It is `gh`-only (no signal for non-GitHub remotes) and `push` has no affected-verification gate; its race gate hard-codes `go test -race ./pkg/...`. | 4 pushes, 0 CI events, no `.agents/events/watchers` activity |
| F3 | `--help` after a subcommand executes the command. | `worktree create --help` made worktree `--help`; `pr create --help` created a branch and **switched the shared root** to it |
| F4 | Verification cache key ignores upstream dependencies (`upstreamKeys` always nil) → stale cached "passed" after a dependency changes. | `runner.go` `ComputePackageKey(pkg, nil, …)` |
| F5 | Go module dependencies are never discovered (`dependencies: null`) → empty graph, `verify --affected` skips dependents, sparse worktrees omit deps. | `graph query libs/money` → no dependents (billing+ledger import it) |
| F6 | `worktree create` checks out a detached HEAD of whatever the root has checked out → PRs get throwaway `feat/pr-<ts>` branches and ids `pr-feat-pr-<ts>-<ts>`. | all 4 PRs |
| F7 | Sparse worktree keeps `go.work` entries for omitted modules → `go build` fails; `subset check` says healthy; `verify` reports a missing package dir as "passed". | agent-ledger, agent-billing |
| F8 | `commit` sweeps MonoFlow state into PR commits (`.agents/cache`, `.agents/prs` symlinks staged by `worktree create`, `circuit_breaker.json`, `subset.json`); root shows them untracked. | `git show --stat` of ledger PR |
| F9 | Cache hit/miss stats are in-memory only → `cache status` always `0/0`. | agent-ui before/after numbers |
| F10 | Risk `TEST_DEFICIT` false positive: JS `test.js` not recognised as a test file. | ui PR scored Tier 2 |
| F11 | Guard `test_weakened` blocks replacing one assertion with a stronger table test. | agent-money commit blocked |
| F12 | `graph query`/`--package` only accept the package name; directory paths fail (`packages/ui`). | agent-ui |
| F13 | Plain JS packages labelled `typescript`. | config |
| F14 | `pr status` says `LOCAL_ONLY` although the branch is on `origin`. | billing, ui |
| F15 | (user report, Claude Code cloud) `pr create` re-checks-out the branch (`git checkout -B`) and depends on an authenticated `gh`; the agent abandoned it for the GitHub connector. | user report |
| F16 | Failed checks dump the entire verbose tool log (≈10k chars of `go test -v`) into the JSON `message`. | dogfooding `verify --affected` on monoflow |
| F18 | `init` commits the platform-specific `.agents/bin/monoflow` binary (12 MB) and the monoflow repo itself tracks `.agents/cache`. | sim repo, monoflow repo |
| F19 | Guard flags prose comments containing "assertion" as commented-out assertions. | blocked the orchestrator's own `monoflow commit` |
| F17 | Agents (incl. the orchestrator) bypass MonoFlow and run raw `go test`/`gofmt`/`git` even with AGENTS.md in context; nothing intercepts them, and `verify` has no format check so raw tools are needed. | orchestrator ran `gofmt`/`go test` directly |

### Prioritised TODO

| Prio | TODO | Covers |
|------|------|--------|
| P0 | Default inner loop runs typecheck **+ lint + test** (`--typecheck`/`--lint`/`--test` narrow it); implement `--all-checks`; DoD evidence lists the commands actually run | F1 |
| P0 | Help flags anywhere never execute a command; real per-command flag help | F3 |
| P0 | Push gate: verify affected packages (all checks) before pushing; drop the hard-coded `./pkg/...` race gate | F1, F2 |
| P0 | CI watcher runs as a detached process; for non-GitHub remotes (or no `gh`) run local CI (`ci_cmd` / full verify of affected) on the pushed SHA and emit `CI_PASSED`/`CI_FAILED` to the shared inbox | F2 |
| P0 | Cache key includes transitive upstream source hashes | F4 |
| P1 | Discover Go `require`/`replace` edges (also fill `null` deps of existing configs); look packages up by path | F5, F12 |
| P1 | `worktree create` makes branch `agent/<id>` from the base branch (`main`), not a detached root HEAD; PR uses the current branch | F6 |
| P1 | Sparse worktrees always include upstream deps and prune `go.work`; missing package dir is a failure, not a pass | F7 |
| P1 | Never stage/commit `.agents/` runtime state; gitignore it at `init` | F8 |
| P2 | Persist cache stats | F9 |
| P2 | Recognise `test.js`/`*.test.*`/`*_test.*`/`test_*.py` as tests in risk scoring | F10 |
| P2 | Guard: only flag deleted assertions when net assertions in the file decrease | F11 |
| P0 | `pr create` never switches branches; opens hosted PR via gh → REST API (`GH_TOKEN`) → explicit `remote_pr` result with compare URL + directive (cloud sandboxes, proxy remotes) | F15 |
| P1 | Compact failure output (failing lines only, capped) | F16 |
| P1 | `monoflow init` installs agent harness hooks (Claude Code PreToolUse, opencode plugin): **hard-block** commands that bypass a gate (git commit/push/pull/checkout, gh pr create, CI polling); **nudge once** for raw build/test/lint (identical retry allowed); gofmt in Go lint | F17 |
| P2 | Code-shaped matching for commented-out assertions | F19 |
| P3 | Don't commit the local monoflow binary; untrack `.agents/cache` in this repo | F18 |
| P3 | JS vs TS language detection; `pr status` reports pushed-to-remote for non-GitHub remotes | F13, F14 |

## Round 2 (2026-10-10) — real GitHub repo + Actions, 6 agents

CI economy: **17 Actions runs for 8 branch heads** (every head ran twice: `push` + `pull_request`), **4/8 heads red**
(ui, api, billing, ledger — all the leaked `go.work`). The round-1 `go vet` trap was caught locally this time (agent fixed it).

Remote CI (GitHub Actions on `M0xoo/monoflow-sim-acme`): **agent-ui and agent-api first pushes FAILED**
(`pattern ./...: directory prefix . does not contain modules listed in go.work`); agents repaired them with follow-up
commits restoring `go.work`. Round-1 issues confirmed fixed: no `--help` side effects, root stayed on `main`,
`agent/<id>` branches, Go dependents found, PRs opened on GitHub (#1–#4), CI_FAILED events delivered.

| # | Finding | Evidence |
|---|---------|----------|
| R2-1 | Sparse worktree's pruned `go.work` is committed (skip-worktree bit does not survive) → broke remote CI. | agent-ui `ea8903e`, agent-api `838789e` |
| R2-2 | Root config files owned by no package (`go.work`, lockfiles, `ci/`, `.github/workflows`) make `verify --affected` report 0 targets → pre-push gate passes a CI-breaking change. | agent-api: `total_targets: 0` for go.work change |
| R2-3 | Skill doc references nonexistent `monoflow stack slice` and `.agents/bin/monoflow`; `pr --help` doesn't say how to satisfy the DoD gate. | agent-api, agent-ui |
| R2-4 | `worktree sync` help/implementation mismatch; no "update my branch from main" command (`sync` fails on unpushed branch). | agent-money-sub |
| R2-5 | Superseded `CI_FAILED` events stay unread/actionable after a newer commit on the same branch passes. | agent-ui |
| R2-6 | Registry `head_sha`/heartbeat frozen at creation; no PR/CI state per worktree. | `worktree list` vs `git worktree list` |
| R2-7 | `pr create` already pushes, so the documented `push` step is redundant (second watcher). | agent-money, agent-money-sub |
| R2-8 | `verify --affected` from the repo root sees nothing when the work is in worktrees (expected) but gives no hint. | agent-money |
| R2-9 | `graph query money` (short name) fails. | agent-money, agent-money-sub |
| R2-10 | Cache stats are global; agents can't attribute hits to their own run. | agent-ui |
| R2-11 | Every head ran CI twice (push + pull_request), superseded runs never cancelled, docs-only/draft changes would run CI; `init --setup-gh-actions` is documented but not implemented. | 17 runs / 8 heads |
| R2-12 | `PruneStale`/`finish` force-delete worktrees with uncommitted/unpushed work (heartbeat only written at create). | code review while fixing R2-6 |
| R2-13 | Shared state dirs (`.agents/prs`, `proofs`) not linked when absent at create → PRs/proofs invisible outside the worktree; `node_modules` symlink shows as untracked. | sim2 worktrees |
| R2-15 | Circuit breaker auto-reverted the working tree to the latest (not necessarily green) checkpoint after 3 failed verifies — silently destroying an iterating agent's edits. | code review |
| R2-16 | Monoflow repo itself tracked `.agents/cache` as a symlink (committed by an agent worktree); `init` committed the 12 MB local binary. | `git ls-files -s` mode 120000 |
| R2-14 | Branch push with no PR (economical CI) would leave the watcher waiting 25 min; `pr create` + `push` start duplicate watchers. | design |

### Prioritised TODO (round 2)

| Prio | TODO | Covers |
|------|------|--------|
| P0 | Commit/checkpoint never stage a projected `go.work` (detect via projection marker) | R2-1 |
| P0 | Unowned root/global files (go.work, lockfiles, root package.json/tsconfig, `ci/`, `.github/workflows`, Makefile) mark **all** packages affected | R2-2 |
| P1 | `monoflow sync` with no args on an agent branch = rebase onto the base branch (main) | R2-4 |
| P1 | New CI result for a branch auto-acks older CI events of that branch | R2-5 |
| P1 | `worktree list` reports live branch/HEAD (from git), PR id and last CI status; heartbeat on every command | R2-6 |
| P2 | Fix skill/AGENTS templates (task slice, ./bin/monoflow, `pr create` pushes, DoD gate hint) | R2-3, R2-7 |
| P2 | `graph query` accepts a unique basename (`money`) | R2-9 |
| P2 | Per-run cache counts in `verify` output (`cached_targets` already) — document; no change | R2-10 |
| P0 | `init --setup-gh-actions`: PR+main triggers only, concurrency cancel, skip drafts/docs, `verify --affected --base=<PR base>` with restored cache; `verify --base` | R2-11 |
| P0 | Never discard unsaved work in `finish`/`gc` (`--force` to override); activity from git | R2-12 |
| P1 | Always link shared state dirs; exclude dependency links | R2-13 |
| P1 | `CI_SKIPPED` when no run appears; one watcher per commit; `pr create` starts it | R2-15 | Circuit breaker auto-reverted the working tree to the latest (not necessarily green) checkpoint after 3 failed verifies — silently destroying an iterating agent's edits. | code review |
| R2-16 | Monoflow repo itself tracked `.agents/cache` as a symlink (committed by an agent worktree); `init` committed the 12 MB local binary. | `git ls-files -s` mode 120000 |
| R2-14 |

## Round 3 (2026-10-10) — 7 agents, real GitHub, MonoFlow-generated CI

**Remote CI never broke: 8/8 pushed heads pass full CI** (round 2: 4/8 red). **One Actions run per code PR**
(6 runs / 6 PRs, all green; round 2: 17 runs / 8 heads), **0 runs for the docs-only PR**. 0 raw build/test or
raw git calls across all agents. CI_PASSED events delivered; `worktree list` showed PR + CI per agent.
Sparse worktrees: 10–13 of 35 files. Cache: 16 hits / 25 misses (39%).

| # | Finding | Evidence |
|---|---------|----------|
| R3-1 | Sparse worktrees project upstream deps but not dependents → verify fails `package_not_projected` until `subset add`. | agent-ui, agent-money-sub |
| R3-2 | Registry written non-atomically without a cross-process lock → concurrent agents lose each other's entries. | agent-docs: `worktree list` → `[]` |
| R3-3 | `events unread` shows every agent's events. | agent-ledger |
| R3-4 | Docs-only change: `total_targets: 0` without explanation. | agent-docs |

## Plug-in on existing public monorepos (`e2e/plugin/check.sh`)

| repo | before | after |
|---|---|---|
| changesets (pnpm, root vitest/eslint) | 24 pkgs, 23 without test cmd, TS labelled JS | 24 TS pkgs, root tools scoped per package |
| opentelemetry-go (nested Go modules) | 9 of 29 modules, 4 edges, **edit → nothing affected** | 29 modules, 138 edges; leaf edit → 1 module (1.7 s), root edit → 27 (9 s warm) |
| tokio (Cargo) | 0 edges | 12 crates, 26 edges |
| langchain (Python, nested) | 6 projects, 0 edges | 22 projects, 71 edges |

| # | Finding |
|---|---------|
| P-1 | Discovery only scanned fixed depths and parsed no Cargo/Python deps; root Go module ignored (CI-protection hole). |
| P-2 | MonoFlow's own files (AGENTS.md, .agents/, .claude/) and docs made the root package "affected". |
| P-3 | Upstream repos have pre-existing failures (otel-go: gofmt) → every verify red on day one. |
| P-4 | `init` skipped an existing AGENTS.md entirely (agents never learn MonoFlow) and ignored CLAUDE.md. |
| P-5 | JS repos running vitest/jest/eslint/tsc from the root: packages had no checks. |

### TODO (round 3 + plug-in) — done in this round
P0 registry lock + atomic writes (R3-2) · P0 manifest discovery at any depth with Go/Cargo/Python/JS dependency parsing (P-1) ·
P0 baseline comparison: pre-existing failures reported, not blocking; `--strict` (P-3) · P1 project dependents (R3-1) ·
P1 non-code files never affect packages, synced with CI paths-ignore (P-2) · P1 merge MonoFlow section into existing
AGENTS.md / CLAUDE.md (P-4) · P1 scope root JS tools per package (P-5). Open: R3-3 inbox scoping, R3-4 hint.

## Versioning between in-repo packages (2026-10-10)

Problems in the original `pkg/version`:

| # | Finding |
|---|---------|
| V-1 | `version check` validated SemVer *syntax* only; consumer ranges were never compared with the versions they resolve to. |
| V-2 | `bump major` always cascaded: every consumer was rewritten onto the breaking version without being migrated. |
| V-3 | Bump type was chosen by hand; nothing detected that an exported symbol was removed. |
| V-4 | No notion of how a dependency is consumed; every producer change re-verified every consumer, so no package could move at its own pace. |
| V-5 | Cargo/pyproject version rewrite also hit `version =` inside `[dependencies.x]`; cascade replaced `workspace:*` with `^x.y.z`. |
| V-6 | **The round-1 cache fix (F4) had never landed** (a silent no-op edit): dependents stayed cached-green after a dependency broke them. Found by the versioning e2e; fixed for verify and deliver, regression-tested, every other reported fix audited. |

Design (implemented): every in-repo edge is **source** (live code) or **pinned** (a MonoFlow internal release:
tag `<slug>@<v>` + snapshot `.agents/releases/<slug>@<v>/`, referenced from the consumer's own manifest).
Only source edges propagate "affected". Producers must version honestly (`version suggest` / DoD-Versioning from the
exported-API diff). Releases notify each consumer with a per-consumer classification (compatible / breaking + the
API changes it uses); consumers upgrade with `version upgrade`, which runs *their* verify. Bumps never rewrite consumers.
Go: a module that pins releases leaves go.work and builds with `GOWORK=off` (Go merges workspace replaces).

E2E (Go sandbox): breaking change on HEAD → only money + billing (source) verified, billing fails until migrated in the
same change; ledger (pinned 1.0.0) untouched. `release 2.0.0` → ledger gets `breaking`, impact `removed Add`, exact
upgrade command; billing gets an informational note. `upgrade` → ledger's own verify fails on `money.Add`; after
migration it passes and the edge reads `pinned 2.0.0`.

## Round 4 (2026-10-10) — versioning agents, real GitHub

MonoFlow CI: 1 run per PR + 1 for main, all green; CI_PASSED delivered to every agent; 0 raw tool calls. Agents used
`version edges / suggest / bump / upgrade` correctly: agent-money-break migrated only billing (source) and bumped major;
agent-ledger-upgrade upgraded its pin 1.0.0 → 1.1.0 to get `Sub`.
**Ground truth (`ci/run.sh`) was red on every head incl. main** once ledger was pinned.

| # | Finding | Fix |
|---|---------|-----|
| R4-0 | Pinned Go module removed from root go.work → plain `go` commands in it fail (repo's own CI red); monoflow hid it with GOWORK=off | module-local `go.work` (`use .`): native tools work, no GOWORK=off |
| R4-1 | `version upgrade` in a sparse worktree: "cannot read module path" when the dependency isn't projected | read go.mod from git |
| R4-2 | Sparse worktrees show ` M go.work` from creation (git clears skip-worktree in sparse checkouts) | assume-unchanged + commit clears it when go.work was really edited |
| R4-3 | `version upgrade` at the repo root silently edited the shared root | edit commands refuse the shared root while worktrees exist (`--allow-root`) |
| R4-4 | `version suggest` → `""` without explanation | note "no changes since …" |
| R4-5 | Every agent's inbox shows every DEPENDENCY_RELEASED | only worktrees containing the consumer |
| R4-6 | `checkpoint revert` doesn't say what it restored | id, message, files |

## Round 5 (2026-10-10) — versioning rerun

MonoFlow CI: one green run per PR + main, CI_PASSED to every agent, 0 raw tool calls. Ground truth still red: the
release snapshot was git-ignored and only materialised by MonoFlow, so a fresh clone's own `ci/run.sh` could not
find `.agents/releases/libs-money@1.0.0`.

| # | Finding | Fix |
|---|---------|-----|
| R5-1 | Ignored snapshots → native tools/CI in a fresh clone fail without a MonoFlow bootstrap | **snapshots are committed** (vendored, immutable, `linguist-generated`); `Prune` drops unreferenced ones; `Tampered` fails `version check` / DoD when a vendored release is edited |
| R5-2 | Sparse worktree hides a pinned dependency's manifest → edge disappears, `version upgrades` = 0 | manifests read from git when not checked out |
| R5-3 | `worktree list` inside a worktree read the worktree's own (empty) registry | manager resolves the main repo root |
| R5-4 | `isolated: false` reads as "not isolated" | `isolation` (own worktree + branch) vs `deps_isolated` |
| R5-5 | Informational release notes for source consumers clutter agent inboxes | only in `--all` |

Validated: fresh clone `ci/run.sh` green; sparse ledger worktree projects only `services/ledger` (pinned money needs no
checkout), sees its compatible upgrade, verifies, clean `git status`.
