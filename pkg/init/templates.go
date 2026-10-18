package initialize

const AgentRuleTemplate = `# MonoFlow (for coding agents)

Run as ` + "`" + `./bin/monoflow` + "`" + `. Four calls take a change to a PR:

1. ` + "`" + `monoflow start <task-id>` + "`" + ` first, then work in the printed ` + "`" + `path` + "`" + ` (your worktree and branch; same files).
   Forgot and edited on main? ` + "`" + `ship` + "`" + ` moves your changes onto their own branch for you.
2. ` + "`" + `monoflow find "<text>"` + "`" + `: every occurrence, grouped code / tests / docs, compact (instead of grep -rn);
   ` + "`" + `monoflow graph query <dir>` + "`" + `: every package importing it. Make the change.
3. ` + "`" + `monoflow verify` + "`" + `: answers in seconds with the changed packages' failures, every one as ` + "`" + `file:line` + "`" + ` with the
   failing line and test; their dependents' tests run in the background meanwhile and the next verify includes
   them. Fix, verify, repeat until PASS (re-runs only what your edits touched). PENDING = your packages pass and
   the dependents are still running: do the remaining work (docs, changeset) meanwhile, then verify again. It formats your changed files
   and bounds itself: no ` + "`" + `timeout` + "`" + `, no ` + "`" + `| tail` + "`" + `, nothing else to run (not vitest / jest / go test / tsc / prettier /
   ` + "`" + `verify --all` + "`" + `). Never poll a log: ` + "`" + `verify` + "`" + ` again waits for a run in progress or prints the last result.
4. ` + "`" + `monoflow ship -m "<msg>"` + "`" + `: commits and returns; once local CI is green it pushes and opens the PR (task
   boundaries are checked there; no ` + "`" + `task check` + "`" + ` needed). Then end your turn: results are delivered to you (on SHIP_FAILED / OUTER_LOOP_REGRESSION: fix, verify, ship).

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
3. make the change → ` + "`" + `monoflow verify` + "`" + ` right away (lists every break, any package, as ` + "`" + `file:line` + "`" + `) → fix those → repeat
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
