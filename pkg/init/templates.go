package initialize

const AgentRuleTemplate = `# MonoFlow (for coding agents)

Run it as ` + "`" + `./bin/monoflow` + "`" + ` (or ` + "`" + `.agents/bin/monoflow` + "`" + `). Four calls take a change from idea to PR:

1. ` + "`" + `monoflow start <task-id>` + "`" + `, then ` + "`" + `cd` + "`" + ` into the printed ` + "`" + `path` + "`" + `: your own worktree and branch
   ` + "`" + `agent/<task-id>` + "`" + ` off the latest main (other agents may be working in the repo root).
2. Find what to change: ` + "`" + `monoflow graph impact <file>` + "`" + ` lists every package, repo-wide, that depends
   on that file (the callers grep would have to find module by module).
3. Edit, then ` + "`" + `monoflow verify` + "`" + `. It builds, lints and tests every changed package and every package
   depending on them: what CI runs for this change, cached. Failures that main already has are
   reported but do not block. Fix the ` + "`" + `errors[]` + "`" + ` (` + "`" + `file:line` + "`" + `) and run it again until it passes.
   Do not also run go test / npm test / tsc / ` + "`" + `verify --all` + "`" + `: ` + "`" + `verify` + "`" + ` already covered them.
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
`

const AgentSkillTemplate = `---
name: monoflow
description: >-
  Use for any edit, build, test, commit, push or PR in this monorepo: start, verify, ship.
---

# MonoFlow

Run as ` + "`" + `./bin/monoflow` + "`" + `. Full guide: ` + "`" + `AGENTS.md` + "`" + `.

1. ` + "`" + `monoflow start <task-id>` + "`" + ` → cd into the printed path
2. ` + "`" + `monoflow graph impact <file>` + "`" + ` → every dependent package, repo-wide
3. edit → ` + "`" + `monoflow verify` + "`" + ` → fix ` + "`" + `errors[].file:line` + "`" + ` → repeat (covers what CI runs; nothing else to run)
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
