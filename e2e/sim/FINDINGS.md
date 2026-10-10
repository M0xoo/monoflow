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
