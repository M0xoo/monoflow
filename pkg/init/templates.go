package initialize

const AgentRuleTemplate = `# MonoFlow (for coding agents)

Run as ` + "`" + `./bin/monoflow` + "`" + `. Four calls take a change to a PR:

1. ` + "`" + `monoflow start <task-id>` + "`" + ` first, then work in the printed ` + "`" + `path` + "`" + ` (your worktree and branch; same files).
   Forgot and edited on main? ` + "`" + `ship` + "`" + ` moves your changes onto their own branch for you.
2. ` + "`" + `monoflow graph query <dir>` + "`" + `: every package, in every module, importing it (instead of grep). Make the change.
3. ` + "`" + `monoflow verify` + "`" + `: builds, lints and tests every package your change can affect (what CI runs for it)
   and lists each break as ` + "`" + `errors[]` + "`" + ` (` + "`" + `file:line` + "`" + `, failing test). Fix those, rerun until it passes.
   Nothing else to run: not go test / npm test / tsc / ` + "`" + `verify --all` + "`" + `, no grepping for affected tests.
4. ` + "`" + `monoflow ship -m "<msg>"` + "`" + `: commits and returns; once local CI is green it pushes and opens the PR.
   End your turn; results are delivered to you (on SHIP_FAILED / OUTER_LOOP_REGRESSION: fix, verify, ship).

More (each ` + "`" + `--help` + "`" + ` explains, never executes): ` + "`" + `checkpoint save|revert` + "`" + ` · ` + "`" + `commit` + "`" + ` / ` + "`" + `push` + "`" + ` / ` + "`" + `sync` + "`" + ` ·
` + "`" + `events unread` + "`" + ` (once, never poll) · ` + "`" + `ci health` + "`" + ` · ` + "`" + `quarantine add` + "`" + ` · ` + "`" + `outer status` + "`" + ` · ` + "`" + `worktree list|finish` + "`" + ` ·
` + "`" + `subset add|remove` + "`" + ` · ` + "`" + `pr status` + "`" + ` · ` + "`" + `review risk` + "`" + ` · ` + "`" + `version edges|suggest|upgrades` + "`" + ` · ` + "`" + `task check` + "`" + ` ·
` + "`" + `guard check` + "`" + ` · ` + "`" + `verify --package=<p>` + "`" + ` · ` + "`" + `help` + "`" + `. Raw git/go/npm only for what MonoFlow does not do; never push to main.
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
