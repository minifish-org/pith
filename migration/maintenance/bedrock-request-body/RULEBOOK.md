# Bedrock request-body maintenance

Use idiomatic Go and keep product builds CGO-free. The Go race detector may use
CGO for diagnostics only. Keep dependency versions, public APIs, AWS SDK
configuration, and unrelated provider behavior unchanged. All generated code,
comments, and notes must be in English.

Only the three registered outputs are writable. Never modify independent
judges, baseline files, historical plans, conformance fixtures, receipts, or
session records. Keep the injected BedrockOptions.Client seam unchanged. Add
focused candidate-owned tests separately from the independent judge.

Implement the narrow request-body adaptation in the contract. Preserve the
resolved AWS HTTP client, its default configuration, configured proxy behavior,
authentication, retries, and streaming response body. Do not change dependency
versions, replace the SDK, buffer responses, swallow errors, retry a failed
response stream, delay request-body cleanup, add arbitrary sleeps, or weaken
the cumulative oracle. Coordinate deterministic tests with channels or mutexes.

Record the local Go transport adaptation in NOTES.md. Do not claim that the
TypeScript implementation contains this Go-specific mechanism or that isolated
counterexample tests prove all production concurrency schedules.
