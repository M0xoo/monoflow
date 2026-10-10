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
  `.agents/bin/monoflow stack slice`
