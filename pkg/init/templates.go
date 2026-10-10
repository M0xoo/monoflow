package initialize

const AgentRuleTemplate = `# MonoFlow Monorepo Protocol (for coding agents)

This repo is governed by MonoFlow. Use it for every build, check, commit, push and PR: it is
faster than raw tools (cached, affected-only), returns compact JSON, and its gates guarantee
remote CI stays green. Run it as ` + "`" + `.agents/bin/monoflow` + "`" + ` (also ` + "`" + `./bin/monoflow` + "`" + ` in worktrees);
` + "`" + `<cmd> --help` + "`" + ` shows flags and never executes anything.

## Workflow
1. ` + "`" + `monoflow worktree create <agent-id> --packages=<pkg,...>` + "`" + ` then ` + "`" + `cd .agents/worktrees/<agent-id>` + "`" + `.
   You get branch ` + "`" + `agent/<agent-id>` + "`" + ` off main with your packages and their dependencies.
   Never edit in the repo root; other agents share it.
2. ` + "`" + `monoflow graph query <pkg|path>` + "`" + ` / ` + "`" + `monoflow graph impact <file>` + "`" + ` — who depends on what (no grepping).
3. Edit. ` + "`" + `monoflow checkpoint save "<idea>"` + "`" + ` before risky attempts, ` + "`" + `monoflow checkpoint revert` + "`" + ` to undo.
4. ` + "`" + `monoflow verify --affected` + "`" + ` — typecheck + lint + tests of changed packages and their dependents.
   Fix the reported ` + "`" + `file:line message` + "`" + ` until it passes. (` + "`" + `--test` + "`" + `/` + "`" + `--lint` + "`" + `/` + "`" + `--typecheck` + "`" + ` narrow it.)
5. ` + "`" + `monoflow commit -m "<msg>"` + "`" + ` — blocked if tests were weakened or secrets added.
6. ` + "`" + `monoflow sync` + "`" + ` — rebase your branch onto the latest main (reports conflicts, never leaves a half rebase).
7. ` + "`" + `monoflow pr create --title="..." --body="..."` + "`" + ` — re-verifies (DoD gate), pushes and opens the PR.
   It is refused while verification fails; ` + "`" + `--draft` + "`" + ` is allowed. Read ` + "`" + `remote_pr` + "`" + ` in the output:
   if it says ` + "`" + `not_created` + "`" + `, follow its ` + "`" + `agent_directive` + "`" + ` (e.g. open it with your GitHub integration).
8. Do not poll CI. ` + "`" + `monoflow commit` + "`" + ` starts the **outer loop** (local CI on that exact commit); ` + "`" + `push` + "`" + ` and
   ` + "`" + `pr create` + "`" + ` only push once it is green. Events (outer loop, remote CI, releases) are **delivered to
   you** by MonoFlow's harness hooks: they appear in your context after tool calls, and when you end your
   turn the hook waits for in-flight results and resumes you if something needs action. Without those hooks:
   run ` + "`" + `monoflow events wait <sha>` + "`" + ` as a background task, or ` + "`" + `monoflow events unread` + "`" + ` when resumed.
   When CI is red, start with ` + "`" + `monoflow ci health` + "`" + ` (failure history, breaking
   commit range, flakiness) instead of reading CI APIs. On CI_FAILED: reproduce with ` + "`" + `monoflow verify --affected` + "`" + `, fix, commit, ` + "`" + `monoflow push` + "`" + `.
9. ` + "`" + `monoflow worktree list` + "`" + ` shows every agent's branch, PR and CI state. ` + "`" + `monoflow worktree finish <agent-id>` + "`" + ` when merged.

## When you need more
| situation | command |
|---|---|
| red remote CI, flaky or recurring failures | ` + "`" + `monoflow ci health [--check=<name> --logs]` + "`" + ` |
| outer-loop status / wait for it | ` + "`" + `monoflow outer list | status <id> | wait <id>` + "`" + ` |
| a test is flaky (not your change) | ` + "`" + `monoflow quarantine add <test-id>` + "`" + ` |
| self-review before a PR (risk, undisclosed critical files) | ` + "`" + `monoflow review risk | review audit` + "`" + ` |
| several PRs touching the same packages | ` + "`" + `monoflow queue spec | queue verify (speculative merge queue)` + "`" + ` |
| combine another agent's work into yours | ` + "`" + `monoflow worktree sync --to=<you> --from=<other> (AST merge)` + "`" + ` |
| need more / fewer packages in your worktree | ` + "`" + `monoflow subset add|remove <pkg> / worktree focus` + "`" + ` |
| build a deliverable artifact of a package | ` + "`" + `monoflow deliver --target=<pkg>` + "`" + ` |
| test a PR or check it out locally | ` + "`" + `monoflow pr test <id> | pr local <id> --worktree | pr status <id>` + "`" + ` |
| split a large change for review | ` + "`" + `monoflow task slice; prove the task: task prove` + "`" + ` |
| scan for secrets / weakened tests | ` + "`" + `monoflow guard check` + "`" + ` |
| cache size and hit ratio | ` + "`" + `monoflow cache status` + "`" + ` |

## Instead of → use
| raw | MonoFlow |
|-----|----------|
| ` + "`" + `go test/build/vet` + "`" + `, ` + "`" + `npm test` + "`" + `, ` + "`" + `tsc` + "`" + `, ` + "`" + `pytest` + "`" + `, ` + "`" + `cargo test` + "`" + ` | ` + "`" + `monoflow verify --affected` + "`" + ` (raw is allowed when you really need it: re-run it) |
| ` + "`" + `git commit` + "`" + ` | ` + "`" + `monoflow commit -m` + "`" + ` |
| ` + "`" + `git push` + "`" + ` | ` + "`" + `monoflow push` + "`" + ` (verifies first; ` + "`" + `main` + "`" + ` is protected) |
| ` + "`" + `git pull` + "`" + ` / ` + "`" + `git rebase main` + "`" + ` | ` + "`" + `monoflow sync` + "`" + ` |
| ` + "`" + `git checkout/switch` + "`" + ` | ` + "`" + `monoflow worktree create` + "`" + ` |
| ` + "`" + `gh pr create` + "`" + ` | ` + "`" + `monoflow pr create` + "`" + ` |
| polling ` + "`" + `gh run watch` + "`" + ` / ` + "`" + `gh pr checks` + "`" + ` | ` + "`" + `monoflow events unread` + "`" + ` once, then stop |

## Versions between packages
Each in-repo dependency is ` + "`" + `source` + "`" + ` (builds against the live code) or ` + "`" + `pinned` + "`" + ` (builds against an
internal release, isolated from later changes): ` + "`" + `monoflow version edges` + "`" + `.
* Changing a package only re-verifies its ` + "`" + `source` + "`" + ` consumers. If ` + "`" + `verify --affected` + "`" + ` fails in one of them,
  migrate it in the same change, or add the new API next to the old one (deprecate, migrate later).
* ` + "`" + `monoflow version suggest <pkg>` + "`" + ` gives the bump your change requires (removed/changed export = major).
* Pinned consumers are notified of releases (DEPENDENCY_RELEASED). ` + "`" + `monoflow version upgrades` + "`" + ` lists them;
  ` + "`" + `monoflow version upgrade <consumer> <dep>` + "`" + ` moves the pin and runs the consumer's checks.
* ` + "`" + `monoflow version pin|unpin <consumer> <dep>` + "`" + ` decouples a consumer from / recouples it to the live code.

Task boundaries: ` + "`" + `.agents/task.json` + "`" + ` (` + "`" + `read_only_paths` + "`" + ` must not be edited); check with ` + "`" + `monoflow task check` + "`" + `.
`

const AgentSkillTemplate = `---
name: monoflow
description: >-
  Use for any edit, build, test, commit, push or PR in this monorepo. MonoFlow gives isolated
  per-agent worktrees, affected-only cached verification with compact JSON, and gates that
  keep remote CI green.
---

# MonoFlow

Run as ` + "`" + `.agents/bin/monoflow` + "`" + `. Full protocol: ` + "`" + `AGENTS.md` + "`" + `. The loop:

1. ` + "`" + `monoflow worktree create <agent-id> --packages=<pkg,...>` + "`" + ` → ` + "`" + `cd .agents/worktrees/<agent-id>` + "`" + `
2. ` + "`" + `monoflow graph impact <file>` + "`" + ` before changing an interface
3. edit → ` + "`" + `monoflow verify --affected` + "`" + ` → fix ` + "`" + `errors[].file:line` + "`" + ` → repeat (` + "`" + `monoflow checkpoint save/revert` + "`" + ` for experiments)
4. ` + "`" + `monoflow commit -m "<msg>"` + "`" + ` → ` + "`" + `monoflow sync` + "`" + ` → ` + "`" + `monoflow pr create --title="..."` + "`" + ` (pushes + opens the PR)
5. ` + "`" + `monoflow events unread` + "`" + ` once, end your turn; act on CI_FAILED when it arrives
`

const TaskTemplate = `{
  "task_id": "task-001",
  "title": "Example Feature",
  "description": "Implement feature in target package without affecting contracts",
  "acceptance_criteria": [
    "Affected unit tests pass",
    "No typecheck errors in downstream packages"
  ],
  "allowed_paths": [],
  "read_only_paths": [],
  "context_files": []
}
`
