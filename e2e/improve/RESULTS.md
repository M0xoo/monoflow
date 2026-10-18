# Improvement loop results (2026-10-18)

`e2e/improve/run.sh`: 5 concurrent opencode agents (`opencode/nemotron-3-ultra-free`) do the
bench tasks (billing, money, ui, ledger, api) on the `e2e/sim` repo with MonoFlow. Each session
ends with `monoflow improve`. Suggestions, retros and triage decisions accumulate in one store
across rounds. Each round also had one Antigravity (gemini) session, run by hand, in its own sim
repo. Between rounds: triage, fix, rebuild, rerun.

| round | retros (unprompted) | suggestions (opencode + antigravity) | branches pushed / red | avg tool calls | avg fresh tokens |
|---|---|---|---|---|---|
| 1 | 5 (0) | 3 + 3, plus 5 found in the transcripts | **3 / 5**, 0 red | 18.2 | 122k |
| 2 | 5 (3) | 1 + 0 | 5 / 5, 0 red | 16.4 | 115k |
| 3 | 5 (5) | 0 + 4 (harder two-PR task) | 5 / 5, 0 red | 16.6 | 105k |
| 4 | 5 (5) | **0 + 0** | 5 / 5, 0 red | 17.8 | 112k |

"Unprompted" = the agent ran the retro on its own. Headless `opencode run` has no Stop hook, so
the harness resumes a session that ended without a retro, using the Stop hook's text.

## What the rounds fixed

| round | finding (source) | fix |
|---|---|---|
| 1 | verify in the clean main checkout passed by diffing `HEAD~1` while the edits were in the agent's worktree; 2 of 5 agents never shipped (transcripts) | `strayWork`: verify/ship in a clean main checkout stop with the worktrees that hold uncommitted work |
| 1 | ship in a clean main checkout returned "shipping" and later failed on an empty `feat/pr-*` branch (agent-api, transcripts) | ship refuses at once, nothing created |
| 1 | the opencode plugin delivered events from the shared checkout (main): agent-api got another agent's SHIP_FAILED and never got its own (transcripts) | the plugin follows the agent's worktree (`start` output, `cd`, `workdir`) |
| 1 | 0/5 agents ran the retro unprompted; ship said "Done: end your turn" (antigravity, harness) | ship's `next` names the retro; AGENTS.md step 5 |
| 1 | `ci health` failed with a raw gh error on non-GitHub remotes (opencode) | says unavailable and points to `events list` |
| 1 | `pr create` without `--title` failed (opencode) | title from the branch's own latest commit |
| 1 | `find` showed test matches as line numbers only (antigravity) | line text for tests when there are ≤ 20 matches |
| 1 | `verify` "3/3 (2 cached)" read as a stale cache (antigravity) | `checked: <pkg> (cached), …` line |
| 1 | `--area=<command\|docs>` was ambiguous: agents passed `--area=command` (antigravity, store) | named areas in the retro text and help |
| 2 | agents went to their final report before the retro | ship's `next` starts with the retro |
| 3 | `find` grouped `test.js` under code (antigravity) | test files recognized by name too |
| 3 | `graph query` named JS dependents without their directory (antigravity) | `@acme/web (apps/web)` |
| 3 | "reused" on the first verify after editing was confusing (antigravity) | the note says the background verify checked these exact files |
| 3 | docs-only verify had no next step (antigravity) | `next: … monoflow ship` |
| 3 | `events unread` hid other branches' events with no hint (antigravity) | hint with `--all` |

Not fixed: "verify should suggest a fix for an unused `fmt.Sprintf` result" (wontfix: the go vet
diagnostic is already exact, and fixing code is the agent's job).

## Reading
- The highest-value findings were not in the agents' retros. Two agents lost their work and said
  "nothing to improve", because MonoFlow told them everything passed. A round's triage must also
  read the journal and transcripts (non-zero exits, redirects, branches that never reached the
  remote). The journal makes that a `jq` call.
- A free model mostly files "none". The stronger Antigravity agent found most of the friction.
  Use both kinds of agent.
