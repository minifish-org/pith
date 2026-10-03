# Race-safe Codemode callback contract

## Source and diagnosis

Pi commit a13d35a742c6ef8462812a28fbe1d8c8b7431c32 invokes JavaScript
callbacks from its ordered host message handler. The callback's synchronous
prefix runs before the first await. Go's Execute(context.Context,
json.RawMessage) callback has no analogous first-await boundary: the complete
blocking callback runs in a goroutine. Independent callbacks can start and finish
in either order. Serializing complete callbacks would break valid overlapping
tool/global operations, so this maintenance explicitly preserves Go concurrency.

The old TestPiV1CodemodeGlobals fixture appends to a shared slice without
synchronization, and assumes the relative side-effect order of two overlapping
callbacks. Both assumptions are invalid for this Go API. This maintenance does
not restore a JavaScript event loop or change the sandbox runtime.

## Required changes

1. Make all shared callback observations in the globals fixture race-safe.
2. Preserve the exact JSON arguments: {"ref":1}, "not awaited", and
   ["classifier",null,3], each exactly once. The first explicitly awaited attach
   must complete before the overlapping calls. Do not assume an order between
   those overlapping calls.
3. Save the initially unawaited attach promise and await its completion before
   checking completion-dependent effects. Check the unchanged script result and
   that only echo appears in Result.Calls. Preserve the independent namespaced
   spread-argument and global-exposure tests elsewhere in the file.
4. Document Tool.Execute and the SDK: callbacks can run concurrently, including
   across concurrent executions, so hosts synchronize shared state; sequential
   await expresses dependency order; Promise.all permits overlap. A sandbox
   return cancels outstanding call contexts but cannot force a host callback
   which ignores context to stop. Do not promise completion or side effects for
   an unawaited callback before Execute returns.
5. Preserve runtime scheduling, JSON bridging, tool recording, VM isolation,
   timeout/cancellation, asset hashes, API signatures and all unrelated tests.

## Independent acceptance

Frozen judges check sequential await ordering with exact JSON payloads, genuinely
overlapping globals, globals excluded from Calls, and cancellation of a pending
unawaited global after its start is established by a barrier. All test recording
must be synchronized. Repeated focused tests, candidate-wide race checks and
whole-project integration must pass. No sleeps or sorted-results-only patch may
be used to disguise the original race.
