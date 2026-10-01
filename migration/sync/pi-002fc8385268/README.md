# First behavioral Pi increment

This reviewed plan migrates the cumulative upstream interval
`f07218c4d4bbc12bef056a7058c3dd49dfe41abe` →
`002fc8385268300ca91a5fc95f935c2afbbdac02`.

The selected feature exposes parsed provider stream events before normalization,
including provider metadata absent from assistant messages. It spans AI adapters,
agent core and the native embedded SDK. Three steps run sequentially in one
module, so a single command executes all steps, verifies the complete project
and commits the accepted implementation. A separate recoverable sync commit
advances the upstream revision only after complete acceptance.

## Review and evidence

- `scope-review.json`: every path in the cumulative Git diff, including paths
  outside the original watch roots, with implementation/adaptation/exclusion reasons.
- `sync-report.json`: unchanged native discovery evidence, not the final write scope.
- `plan.json` / `workflow.json`: narrowed reviewed ownership and executable steps.
- `new-mappings.json`: exact ownership corrections applied after acceptance.
- `contracts/`: frozen behavioral requirements and idiomatic Go adaptations.
- `judges/`: independent public-API/protocol acceptance tests, compiled on old APIs.
- `VALIDATION.md`: positive controls, expected negative controls and preflight evidence.

There are 20 existing writable Go files and four new candidate outputs (three
self-test files and a SDK guide). The other 1,025 baseline files are read-only.
No existing conformance judge, receipt, dependency manifest or catalog data is writable.
The module does not claim to port the entire Pi monorepo. Pico3/Chord delta/state,
npm extension installation, TUI and service/storage packages remain outside scope.

## Execute from Pith

Preparation and preflight do not call models. Run this first if desired:

```sh
../portsmith-go/bin/portsmith sync \
  --upstream 002fc8385268300ca91a5fc95f935c2afbbdac02 --check
```

Then run the actual migration with the existing DeepSeek configuration:

```sh
../portsmith-go/bin/portsmith sync \
  --upstream 002fc8385268300ca91a5fc95f935c2afbbdac02 \
  --commit --env-file ../omni-pi/.env
```

The operator starts the paid model run. Repeat the same command to resume after
interruption or a generation failure; do not delete the journal or regenerate the
plan. The executor commits accepted work but does not push it. After completion,
inspect the receipt and run `git push` yourself.

The rejected `pi-9a139c62bf6b` draft is retained as discovery/audit evidence;
it is not an executable acceptance plan and never advances the baseline.
