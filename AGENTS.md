# MonoFlow Agentic Monorepo Protocol

You are operating inside a governed monorepo equipped with **MonoFlow**.
Follow these strict rules to keep execution fast, prevent context blowup, and avoid state corruption:

## 1. Zero File Crawling — Use the Dependency Graph
* **NEVER** recursively grep or crawl directories across the repository to guess dependencies.
* Run: `.agents/bin/monoflow graph query <package>` to inspect exact upstream imports and downstream dependents.
* Run: `.agents/bin/monoflow graph impact <file>` before modifying an interface to know which packages will be affected.

## 2. Ephemeral Checkpoints — Never Dirty Git History
* Standard `git commit` is strictly prohibited for experimental trial-and-error edits.
* Before attempting a refactor or non-trivial edit, run:
  `.agents/bin/monoflow checkpoint save "hypothesis description"` (takes ~15ms).
* If an edit fails verification, instantly rollback state with:
  `.agents/bin/monoflow checkpoint revert` (takes ~20ms).

## 3. Sub-2s Inner Loop — Affected Checks Only
* **NEVER** run the entire repository's test suite or global Docker builds during code generation.
* Run: `.agents/bin/monoflow verify --affected`
* Output is emitted in compact JSON (`file`, `line`, `col`, `code`, `message`) without ANSI color codes or terminal noise.

## 4. Respect Task Boundaries
* Check declared boundaries in `.agents/task.json`.
* Do NOT edit files marked in `read_only_paths`.
* Run: `.agents/bin/monoflow task check` to ensure you did not modify forbidden paths.

## 5. Sliced Stacked Diffs
* When completing a task, slice changes into stacked review tiers using:
  `.agents/bin/monoflow task slice`

## 6. Zero Raw Git Commands — Use MonoFlow Exclusively
* **NEVER** run raw `git commit`, `git push`, `git pull`, or `git checkout`.
* Use MonoFlow commands:
  - Commit: `monoflow commit -m "..."` (sub-50ms verified commit)
  - Push: `monoflow push` (enforces protected branch shield and pre-push race detector)
  - Sync: `monoflow sync` (sub-second fast-forward / rebase respecting sparse subsets)
  - Subsetting: `monoflow subset set|add|remove|update|check`

## 7. Protected Branch Shield & Agent-Native PR Lifecycle
* Direct pushes to `main` and `master` are strictly blocked.
* Propose all changes via `monoflow pr create --title="..."`.
* Check bridged status: `monoflow pr status [id]` (shows local machine DoD proof receipt & remote GitHub URL in one view).
* Test PRs with isolated hot-mount delta testing: `monoflow pr test <id>`.
* Project affected packages locally: `monoflow pr local <id> [--worktree]`.
* Sign-off and merge: `monoflow pr review <id> --approve`, `monoflow pr merge <id> --strategy=squash`.

## 8. Zero CI Polling & Reactive Asynchronous Wakeup
* **STRICT PROHIBITION**: NEVER poll `gh pr checks`, `gh run watch`, or schedule artificial polling loops / timer wakeups while waiting for CI.
* MonoFlow operates on a **fire-and-forget** reactive notification model:
  - `monoflow push` / `monoflow pr create` start a detached watcher that writes CI_PASSED / CI_FAILED / CI_SKIPPED to the local inbox (`.agents/events/`).
  - To be woken by the result, run `monoflow events wait <sha>` (printed as `ci_wait_command`) as a **background task** (Claude Code: Bash with `run_in_background`) and end your turn: it exits when the result lands (0 = passed/skipped, 1 = failed, with logs), which wakes you.
  - Harnesses without background tasks: end your turn and run `monoflow events unread` when resumed. Nothing resumes you on its own.
* **When CI is red**, start with `monoflow ci health` (add `--check=<name> --logs` to focus) instead of reading GitHub run/job APIs one by one: it reports when each check started failing, the breaking commit range, the fixing commit of earlier incidents, recurrences and flakiness.

## 9. Agents Must Default to Isolated Worktrees
* **STRICT ISOLATION CONTRACT**: Autonomous agents must NEVER develop directly in the shared repository root (`.`) when concurrent agents or humans may be operating.
* **Worktree-First Lifecycle**:
  1. **Provision**: At the start of a task, create an isolated virtual worktree focused on the required packages:
     `monoflow worktree create <task-id-or-agent-id> --packages=<focused_packages>`
  2. **Operate**: Execute all edits, inner loop verifications, ephemeral checkpoints, and commits inside `.agents/worktrees/<agent-id>`.
  3. **Propose**: Run `monoflow pr create` from within the worktree directory so the PR diff contains exclusively the scoped changes.
  4. **Tear Down**: Once merged, reclaim disk space with `monoflow worktree finish <agent-id>`.

