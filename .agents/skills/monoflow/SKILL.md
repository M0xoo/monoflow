---
name: monoflow
description: >-
  Use this skill whenever navigating, editing, verifying, or committing code in this monorepo.
  Provides sub-second inner loop verification, ephemeral snapshots, and dependency graph queries.
---

# MonoFlow Autonomous Workflow

Follow this procedure for all coding tasks:

## Procedure

1. **Inspect Boundaries and Impact**:
   - Check `.agents/task.json` for allowed paths.
   - Run `.agents/bin/monoflow graph query <target>` to see what depends on the target package.

2. **Save a Working Checkpoint**:
   - Run `.agents/bin/monoflow checkpoint save "initial state"`.

3. **Perform Code Modifications**:
   - Edit target files within allowed paths.

4. **Execute Inner Loop Verification**:
   - Run `.agents/bin/monoflow verify --affected`.
   - If status is "failed", inspect the JSON error output:
     ```json
     { "file": "path", "line": 42, "code": "TS2322", "message": "..." }
     ```
   - If fix fails repeatedly, run `.agents/bin/monoflow checkpoint revert` to restore clean state.

5. **Finalize Task**:
   - Verify task boundaries: `.agents/bin/monoflow task check`.
   - Slice changes into stacked review commits: `.agents/bin/monoflow stack slice`.
