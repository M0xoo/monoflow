package initialize

const AgentRuleTemplate = `# MonoFlow (for coding agents)

Run it as ` + "`" + `./bin/monoflow` + "`" + ` (or ` + "`" + `.agents/bin/monoflow` + "`" + `). Four calls take a change from idea to PR:

1. First, before reading any code: ` + "`" + `monoflow start <task-id>` + "`" + `, then ` + "`" + `cd` + "`" + ` into the printed ` + "`" + `path` + "`" + ` and work
   only there (your worktree and branch ` + "`" + `agent/<task-id>` + "`" + ` off the latest main; same files as the root).
2. Who uses what you change: ` + "`" + `monoflow graph query <dir>` + "`" + ` lists every package, in every module, that
   imports it (code or tests), instead of grepping the repo. Then make the change.
3. Run ` + "`" + `monoflow verify` + "`" + ` right away instead of searching for what your change breaks: it builds, lints and
   tests every changed package and every package depending on them (what CI runs for this change,
   cached) and lists each break as ` + "`" + `errors[]` + "`" + ` with ` + "`" + `file:line` + "`" + ` and the failing test, in any package.
   Fix exactly those and run it again until it passes. Failures main already has do not block.
   Do not also run go test / npm test / tsc / ` + "`" + `verify --all` + "`" + ` or grep for affected tests: ` + "`" + `verify` + "`" + ` covers them.
4. ` + "`" + `monoflow ship -m "<commit message>"` + "`" + ` (optional ` + "`" + `--title=... --body=...` + "`" + `): commits, runs CI locally on
   that exact commit, pushes and opens the PR. Then end your turn: CI results are delivered to you
   (on CI_FAILED: fix, ` + "`" + `verify` + "`" + `, ` + "`" + `ship` + "`" + ` again).

Use raw git/go/npm only for what MonoFlow does not do; never push to main.

## When you need more
| situation | command |
|---|---|
| dependencies / dependents of a package | ` + "`" + `monoflow graph query <package|path>` + "`" + ` |
| try something risky | ` + "`" + `monoflow checkpoint save "<idea>"` + "`" + ` / ` + "`" + `checkpoint revert` + "`" + ` |
| one package, or everything | ` + "`" + `monoflow verify --package=<name|path>` + "`" + ` / ` + "`" + `verify --all` + "`" + ` |
| commit without a PR yet / push / pull | ` + "`" + `monoflow commit -m` + "`" + ` / ` + "`" + `push` + "`" + ` / ` + "`" + `sync` + "`" + ` |
| CI result without hooks | ` + "`" + `monoflow events unread` + "`" + ` (once; never poll) |
| red/flaky CI history | ` + "`" + `monoflow ci health [--check=<name> --logs]` + "`" + ` |
| flaky test (not yours) | ` + "`" + `monoflow quarantine add <test-id>` + "`" + ` |
| local CI state | ` + "`" + `monoflow outer list | status | wait` + "`" + ` |
| other agents, merged work | ` + "`" + `monoflow worktree list` + "`" + ` / ` + "`" + `worktree finish <task-id>` + "`" + ` / ` + "`" + `worktree sync --to=<you> --from=<other>` + "`" + ` |
| fewer packages in a big repo | ` + "`" + `monoflow start <id> --packages=<p1,p2>` + "`" + ` / ` + "`" + `subset add|remove <pkg>` + "`" + ` |
| PR status / review | ` + "`" + `monoflow pr status <id>` + "`" + ` / ` + "`" + `review risk` + "`" + ` |
| package versions between packages | ` + "`" + `monoflow version edges | suggest <pkg> | upgrades` + "`" + ` |
| task boundaries, secrets, weakened tests | ` + "`" + `monoflow task check` + "`" + ` / ` + "`" + `guard check` + "`" + ` |
| anything else | ` + "`" + `monoflow help` + "`" + `, ` + "`" + `monoflow <cmd> --help` + "`" + ` (help never executes) |

The rest of this file, if any, is the project's own guide.
`

const AgentSkillTemplate = `---
name: monoflow
description: >-
  Use for any edit, build, test, commit, push or PR in this monorepo: start, verify, ship.
---

# MonoFlow

Run as ` + "`" + `./bin/monoflow` + "`" + `. Full guide: ` + "`" + `AGENTS.md` + "`" + `.

1. ` + "`" + `monoflow start <task-id>` + "`" + ` → cd into the printed path
2. ` + "`" + `monoflow graph query <dir>` + "`" + ` → every package, any module, importing it (instead of grep)
3. make the change → ` + "`" + `monoflow verify` + "`" + ` right away (lists every break, any package, as ` + "`" + `errors[].file:line` + "`" + `) → fix those → repeat
4. ` + "`" + `monoflow ship -m "<msg>"` + "`" + ` → commit, local CI, push, PR; end your turn
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
