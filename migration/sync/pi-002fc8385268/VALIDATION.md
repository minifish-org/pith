# Preparation validation

No implementation model has run. All checks use local fake providers or temporary
Git repositories with `CGO_ENABLED=0` and bounded Go build parallelism.

## Independent judges before implementation

The frozen judges compile against the accepted Pith baseline. All eleven HTTP/SSE
or AWS event-stream protocol fixtures pass without the new observer. The baseline
passes the Copilot Opus 5.5 catalog guard. A local Codex WebSocket positive control
checks that its fixture works without HTTP fallback.

Feature judges deliberately fail on the old baseline: the public observer field
and SDK provider event are absent. This is an expected negative control, not a
completed migration. The exact feature behavior remains for Portsmith/Pith/DeepSeek
to implement and verify. Judges run again cumulatively after each step.

Checks cover only the selected contract. They do not prove equivalence of all
provider payloads, live authentication/services or the entire Pi monorepo.

## Native sync ownership fix

Regression tests distinguish implementation sources from reference-only inputs:
changing a watched TS test is reported for review, without granting writable Go
ownership or incorrectly treating a reviewed reference as an unknown new source.
New accepted ownership removes that source from the reference-only list.

## Final preflight

The native `sync --check` result is saved beside this document as
`preflight.json` once successful. It validates frozen sources, manifests,
contracts, judge names, output ownership and all-prepared execution. It does not
spend provider credits or implement the feature.
