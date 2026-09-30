# Ongoing porting: map upstream changes to reviewed Go updates

The first migration pins Pi through upstream.json. The original inventory maps source hashes, target paths, batches and symbols. Accepted module receipts add target hashes and verification evidence. The SDK increment additionally records actual Go source files and exported declarations in its source maps. The current accepted SDK commit is `887fbc1a5b402a3fdfbb9636a17855cfeeea12f7`.

## Review cycle

1. Preserve the accepted Pi source and Go commits. Fetch a named new upstream revision into a separate verified directory, keeping its license and original cache.
2. Compare source, tests, fixtures, scripts, package/configuration files and generated data. Report additions, removals and renames; an identical-content rename is a hint, not semantic proof.
3. Follow source mappings into Go, then reverse dependencies and shared contracts. AI changes need Core/Tools regressions; Core changes need downstream tool regressions. Include the embedded SDK when its dependencies change.
4. An external planner reviews behavior and dependency changes, updates contracts/judges/mappings, and prepares an incremental plan. New files remain unclassified until reviewed. Report out-of-scope changes explicitly.
5. Execute the reviewed plan without discarding accepted code or downstream customization. Portsmith currently supports additive baselines; arbitrary modification/deletion and three-way merging of accepted files still need explicit engineering and review.
6. Accept only after affected checks pass. Record per-module upstream revisions, target hashes, tests and commits. Do not advance a global revision ahead of unfinished modules.

## Read-only comparison helper

```sh
node ../portsmith/node_modules/tsx/dist/cli.mjs migration/upstream-diff.mts --source /absolute/path/to/new/pi
```

The helper does not download, call a model, rewrite plans, port code or commit. It reports changed/added/deleted files, possible identical-content renames, affected/regression modules, unclassified files and generated catalog changes against the original inventory. The SDK inventory must also be reviewed separately; the original helper is not a complete SDK incremental planner.

Keep upstream repository/commit/tag, source and artifact hashes, plan/contract/judge/dependency hashes, Go base/accepted commits, symbol coverage, test/platform/live-smoke status and intentional differences. Compare target hashes before updating; use base-to-upstream and base-to-downstream comparisons to preserve customizations. Removed APIs need consumer checks and migration notes. Splits and merges retain many-to-many provenance.

Automatic weekly updates, semantic planning, arbitrary downstream merges and automatic push are not implemented. A reviewed manual cycle is the current workflow.
