package initialize

const AgentRuleTemplate = `# MonoFlow Monorepo Protocol (for coding agents)

Use MonoFlow (` + "`" + `.agents/bin/monoflow` + "`" + `, ` + "`" + `./bin/monoflow` + "`" + ` in worktrees) for builds, checks, commits, pushes
and PRs: cached, affected-only, compact JSON, and its gates keep remote CI green. ` + "`" + `<cmd> --help` + "`" + ` never executes.

## Workflow
1. ` + "`" + `monoflow worktree create <agent-id> --packages=<pkg,...>` + "`" + `, then ` + "`" + `cd .agents/worktrees/<agent-id>` + "`" + `
   (branch ` + "`" + `agent/<agent-id>` + "`" + ` off main; never edit the shared repo root).
2. ` + "`" + `monoflow graph query <pkg|path>` + "`" + ` / ` + "`" + `graph impact <file>` + "`" + `: dependents and dependencies, instead of grepping for them.
3. Edit. ` + "`" + `monoflow checkpoint save "<idea>"` + "`" + ` before risky attempts, ` + "`" + `checkpoint revert` + "`" + ` to undo.
4. ` + "`" + `monoflow verify --affected` + "`" + ` (instead of go test, npm test, tsc, pytest, cargo test): typecheck + lint +
   tests of changed packages and their dependents. Fix the reported ` + "`" + `file:line` + "`" + ` errors until it passes.
5. ` + "`" + `monoflow commit -m "<msg>"` + "`" + ` (not git commit). It starts the outer loop: local CI on that exact commit.
6. ` + "`" + `monoflow pr create --title="..." --body="..."` + "`" + ` (not gh pr create) right after: it waits for the outer
   loop, pushes and opens the PR. If ` + "`" + `remote_pr` + "`" + ` says ` + "`" + `not_created` + "`" + `, follow its ` + "`" + `agent_directive` + "`" + `.
   Also: ` + "`" + `monoflow sync` + "`" + ` (not git pull/rebase), ` + "`" + `monoflow push` + "`" + ` (not git push; main is protected).
7. Do not poll CI. Results are delivered to you by hooks after tool calls and when you end your turn.
   Without hooks: ` + "`" + `monoflow events wait <sha>` + "`" + ` in the background, or ` + "`" + `monoflow events unread` + "`" + ` once.
   On CI_FAILED: ` + "`" + `monoflow ci health` + "`" + `, reproduce with ` + "`" + `verify --affected` + "`" + `, fix, commit, push.
8. ` + "`" + `monoflow worktree list` + "`" + ` (all agents' branch/PR/CI); ` + "`" + `monoflow worktree finish <agent-id>` + "`" + ` once merged.

## When you need more
| situation | command |
|---|---|
| red/flaky CI history | ` + "`" + `monoflow ci health [--check=<name> --logs]` + "`" + ` |
| outer-loop state | ` + "`" + `monoflow outer list | status | wait` + "`" + ` |
| flaky test (not yours) | ` + "`" + `monoflow quarantine add <test-id>` + "`" + ` |
| self-review before a PR | ` + "`" + `monoflow review risk | review audit` + "`" + ` |
| several PRs on the same packages | ` + "`" + `monoflow queue spec | queue verify` + "`" + ` |
| merge another agent's work | ` + "`" + `monoflow worktree sync --to=<you> --from=<other>` + "`" + ` |
| more/fewer packages in the worktree | ` + "`" + `monoflow subset add|remove <pkg>` + "`" + ` / ` + "`" + `worktree focus` + "`" + ` |
| build a deliverable | ` + "`" + `monoflow deliver --target=<pkg>` + "`" + ` |
| PR test / checkout / status | ` + "`" + `monoflow pr test <id> | pr local <id> --worktree | pr status <id>` + "`" + ` |
| split or prove a task | ` + "`" + `monoflow task slice | task prove` + "`" + ` |
| secrets / weakened tests | ` + "`" + `monoflow guard check` + "`" + ` |
| cache stats | ` + "`" + `monoflow cache status` + "`" + ` |

## Versions between packages
Edges are ` + "`" + `source` + "`" + ` (live code) or ` + "`" + `pinned` + "`" + ` (an internal release): ` + "`" + `monoflow version edges` + "`" + `. A change
re-verifies only ` + "`" + `source` + "`" + ` consumers; if one fails, migrate it in the same change or add the new API next
to the old. ` + "`" + `version suggest <pkg>` + "`" + ` gives the required bump. Pinned consumers get DEPENDENCY_RELEASED:
` + "`" + `version upgrades` + "`" + `, ` + "`" + `version upgrade <consumer> <dep>` + "`" + `, ` + "`" + `version pin|unpin <consumer> <dep>` + "`" + `.

Task boundaries: ` + "`" + `.agents/task.json` + "`" + ` (` + "`" + `read_only_paths` + "`" + `); check with ` + "`" + `monoflow task check` + "`" + `.
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
