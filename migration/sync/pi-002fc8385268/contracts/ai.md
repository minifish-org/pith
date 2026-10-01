# Parsed provider stream event observer

Upstream increment: `f07218c4d4bbc12bef056a7058c3dd49dfe41abe` to
`002fc8385268300ca91a5fc95f935c2afbbdac02`.

## Public Go contract

Expose `OnProviderStreamEvent func(data any, model *types.Model) error` through
`types.StreamOptions` and `types.SimpleStreamOptions`, preferably as a field of
embedded `ProviderRequestOptions`. Preserve it in `BuildBaseOptions` and every
simple-to-provider options conversion. A nil callback preserves existing behavior.
No new dependencies, no CGO, no credentials or live model requests in tests.

Invoke it once per parsed provider event, in stream order, before that event's
normalization. Invoke it even for metadata-only and terminal events consumed by
the adapter. Exclude SSE framing and `[DONE]` sentinels. The callback receives
the selected model identity and read-only adapter-owned data; it is an observer,
not a payload replacement hook. Map-based adapters must retain provider-specific
fields, including unknown JSON metadata absent from the normalized assistant.

Cover Anthropic, OpenAI completions (including OpenRouter), OpenAI Responses,
Azure Responses, Codex Responses SSE and WebSocket, Google Generative AI, Google
Vertex, Mistral and Pi Messages, plus Bedrock Converse. Reuse shared Go consumers
when appropriate. Pi Messages currently decodes into a narrow Go struct: retain
the parsed JSON object for observation before its typed conversion so unknown
provider fields are not lost. Existing public PiMessagesEvent behavior stays
compatible. Google and Bedrock may expose their parsed Go SDK/adapter response
structs; the contract does not promise fields their decoder/SDK already discards.
Document this distinction from original HTTP bytes.

## Ordering, errors and lifecycle

The callback is synchronous: finish it before observing or normalizing the next
event. A blocked callback prevents stream completion and further callbacks.
Return errors through the existing error event/result with the original error
text. Stop observing/normalizing subsequent events. Never treat an observer
error as a transient transport error: no request retry, WebSocket retry, SSE
fallback or cached-session fallback activation because of that error. Genuine
transport retry policy remains unchanged. Close streams/bodies and release
WebSocket state on all paths; ensure a blocked SSE producer can exit on cancellation.
Raw events are not appended to final assistant content or transcript.

## Frozen acceptance and candidate tests

`TestPortsmithJudgeProviderEvents` covers eleven local protocol fixtures: successful
ordered observation/model identity/raw metadata where applicable, no-observer
compatibility, and observer error propagation without replay. Separate judges
cover backpressure, Codex WebSocket no-fallback and positive fixture controls.
The Copilot effort metadata judge guards a cumulative generator-only change
already satisfied by the frozen target catalog. Preserve that catalog.

Write meaningful self-tests in `provider_stream_events_test.go`: include direct
provider-options use, option forwarding, metadata-only events, error cleanup and
cancellation, not just field existence. Frozen judges are read-only. Existing
regression tests and catalog data are read-only and must continue to pass.

## Scope

The complete cumulative path review is in `scope-review.json`. Chord delta/state,
Pico3, durable storage, npm extension installation, TUI and remote services remain
excluded by the original migration scope. Do not expand this increment into them.
Record idiomatic adaptations in the authorized SDK guide; never claim full Pi parity.
