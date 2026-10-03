# Chord dependencies required by Pi Durable

This step ports the complete Chord runtime dependency closure needed by the
Durable SDK at Pi revision `a13d35a742c6ef8462812a28fbe1d8c8b7431c32`.
It adds strict JSON ownership, decoded delta operations, transactional revision
tracking, and source-attached publication. It preserves the already shipped
`packages/chord/context` API and uses standard `context.Context` at SDK boundaries.

## Source closure and scope

Read these complete files, while porting only the dependency slice identified here:

| Upstream file | Lines | Required responsibility |
| --- | ---: | --- |
| `packages/chord/src/index.ts` | 85 | Public JSON/state/type exports |
| `packages/chord/src/api.ts` | 110 | Source form of `replicatedState` |
| `packages/chord/src/types.ts` | 339 | JSON, context, source attachment, publication contracts |
| `packages/chord/src/json.ts` | 120 | Strict JSON validation and alias-free copying |
| `packages/chord/src/context/index.ts` | 121 | Existing Go API compatibility |
| `packages/chord/src/delta/index.ts` | 694 | Operations, safety, overlap, apply, immutable replay, codec |
| `packages/chord/src/delta/tracker.ts` | 2,205 | Draft, prepared revision, ownership and adoption |
| `packages/chord/src/delta/diff.ts` | 523 | Correct revision diff generation |
| `packages/chord/src/delta/draft.ts` | 10 | Mutable draft type adaptation |
| `packages/chord/src/delta/apply-immutable-trusted.ts` | 128 | Trusted replay ownership rules |
| `packages/chord/src/delta/revision-validator.ts` | 75 | Strict immutable revision validation |
| `packages/chord/src/services/state.ts` | 457 | Attached source and independent serialized delivery |
| `packages/chord/src/services/state-internals.ts` | 19 | Snapshot/publication ordering contract |

The complete reference files contain 4,886 lines. This is not a requirement to
reimplement every declaration in these files. In particular, Durable imports no
Chord facet host, remote-service protocol, JavaScript bundler, or Node loader.
Those independent Chord products and optional facet/remote integrations shown in
`packages/durable/test/chord-guide.test.ts` are deferred explicitly. All source
attachment behavior used by document states, conversation views, and task graphs
is in scope.

Selected behavior references are Chord `json.test.ts`, `context.test.ts`,
`delta.test.ts`, `delta-apply-immutable.test.ts`, `delta-diff.test.ts`,
`delta-clone.test.ts`, `delta-tracker/tracker.test.ts`, `state.test.ts`, and
`state-delivery.test.ts` (3,417 lines in total). Durable document/watch/live-delta
and guide tests are additional cross-module references. Benchmarks, weak-reference
retention metrics, and JavaScript-specific object identity are not correctness
requirements for the Go port.

## Public Go surface

Use `github.com/minifish-org/pith/packages/chord` and its `delta` subpackage.
Public signatures below are fixed for downstream Durable steps and independent
judges. Additional helpers are allowed without replacing these signatures.

```go
// packages/chord
type JSONValue = any
type Op = delta.Op
type Path = delta.Path
func CopyJSON(value any) (any, error)
func IsJSONValue(value any) bool

type StateSnapshot struct { Value any; Cursor uint64 }
type StateFrame struct {
    Value any
    Cursor uint64
    Ops []delta.Op
    Context context.Context
}
type StateSource interface { Attach() (StateAttachment, error) }
type StateAttachment interface {
    Snapshot() StateSnapshot
    Activate(func(StateFrame)) error
    Dispose()
}
type StateDelivery struct { Kind string; Sequence uint64 }
type StateListener func(any, context.Context, StateDelivery) error
type StateOptions struct { OnError func(error) }
func AttachReplicatedState(StateSource, StateOptions) (*AttachedState, error)
func (*AttachedState) Value() any
func (*AttachedState) Subscribe(StateListener) (func(), error)
func (*AttachedState) Dispose()

// packages/chord/delta
type Path []any
type Op []any
func Apply(any, []Op) (any, error)
func ApplyImmutable(any, []Op) (any, error)
func ApplyImmutableBatches(any, [][]Op) (any, error)
func DiffRevisions(any, any) ([]Op, error)
func Overlap(a, b string, scan int) int
func Track(any) (*Tracker, error)
func (*Tracker) Value() any
func (*Tracker) Revision() uint64
func (*Tracker) BeginChange() (*Change, error)
func (*Tracker) PrepareReplace(any) (*Prepared, error)
func (*Tracker) Adopt(*Prepared) error
func (*Change) Value() (any, error)
func (*Change) Prepare() (*Prepared, error)
func (*Change) Abort()
func (*Prepared) Base() any
func (*Prepared) Value() any
func (*Prepared) Ops() []Op
func (*Prepared) BaseRevision() uint64
func (*Prepared) Abort()
type WireOp []any
func NewEncoder() *Encoder
func (*Encoder) Encode([]Op) ([]WireOp, error)
func NewDecoder() *Decoder
func (*Decoder) Decode([]WireOp) ([]Op, error)
func IsBase([]Op) bool
func IsReplace(Op) bool
```

## JSON and tuple safety

Canonical runtime JSON containers are `map[string]any` and `[]any`, with null,
booleans, strings, and finite numeric values. Copying must duplicate each
container occurrence: a shared input child becomes independent output children.
Detect ancestor cycles, reject non-finite numbers, functions, channels, and
unsupported objects. Strict JSON is a runtime boundary, not merely `any`.
Go numeric primitives may normalize to JSON numbers; correctness comparisons
are by JSON value rather than a particular Go numeric representation.

Persist and expose operations as the exact decoded Pi tuple vocabulary:
`r`, `s`, `d`, `a`, `t`, `p`, and `m`. Retain numeric array indices and string
object keys as distinct path segments. No path may contain `__proto__`,
`constructor`, or `prototype`. Reject malformed arity, unknown verbs, fractional
or negative indices, unresolved paths, non-array splice/permutation targets,
non-bijective permutations, and sparse-array writes. Root replacement is `r`;
root `s`, `d`, `a`, or `t` is invalid. Root array splice and permutation are legal.

`a` appends a string. `t` removes the requested number of **UTF-16 code units**
from the start, matching upstream JavaScript and on-disk tuple compatibility.
`Overlap` returns UTF-16 units too, honors the requested scan window, and must
never claim a suffix/prefix match that is false. Do not use raw UTF-8 byte counts
for stored string offsets. Go cannot represent isolated UTF-16 surrogates as
valid UTF-8 strings: reject a truncation that bisects a supplementary character
and document this narrow adaptation rather than corrupting persisted data.

`Apply` may mutate a caller-owned target and adopt tuple payload ownership.
`ApplyImmutable` and batch replay must leave earlier revisions unchanged.
No exact operation minimization strategy or internal structural sharing is
required: replay of emitted operations must equal the prepared value. No-op
diffs and unchanged draft preparation must emit an empty operation batch.

## Transactional tracking

Track accepts only JSON object/array roots. A change holds a private mutable
working copy. Preparation validates and detaches that copy, records its matching
base revision, and leaves the tracker unchanged. An aborted candidate never
changes authority. Adoption is the commit boundary: require the same owner,
current base revision, prepared status, and one-time consumption. Reject foreign,
stale, aborted, and previously adopted candidates. Adoption advances revision
once, including an accepted no-op candidate; competing open/prepared changes
then become stale.

Go cannot revoke an escaped map reference like a JavaScript Proxy. The explicit
Go adaptation is to invalidate the change handle, detach candidate data during
preparation, and prevent mutations of retained draft maps or public candidate
getters from changing committed revisions. Value getters must return independent
copies where needed to enforce this isolation. Callers own drafts while editing
and use `CopyJSON` when assigning externally owned data. Errors are returned
explicitly; no implementation may silently turn an invalid draft into a no-op.

## Source-attached publication

Attachment atomically captures an immutable value and a source cursor. Activation
installs one listener and drains buffered frames in source order. A frame cursor
must be exactly the previous source cursor plus one. A gap, duplicate, or invalid
JSON revision stops/disposes that attachment and reports the error through
`StateOptions.OnError`; it must not overwrite the last valid value. If activation
fails, dispose the attachment before returning the error.

An attached state's publication sequence starts at zero. Each subscriber first
receives hydration at the current publication sequence, then future updates in
increasing sequence order. Every subscription serializes its callbacks
independently. Go callback workers may begin asynchronously; a slow subscriber
must not block the source or another subscriber. Preserve a maximum of 100
pending deliveries: when that backlog overflows, coalesce pending updates to
the newest revision while retaining a not-yet-started hydration. This is an
upstream correctness rule, not a new arbitrary migration limit.

Report callback errors in isolation and keep delivering subsequent revisions.
Unsubscribe discards pending work without cancelling or joining an active
callback. Dispose is idempotent, releases the source attachment once, ignores
later source frames, and preserves the last value for reading. Propagate the
provided invocation context for updates; hydration uses a background context.
Source frames carry authoritative revisions: publication must not re-diff or
re-apply their operations as an alternative authority.

## Output ownership and implementation freedom

Recommended new files are `packages/chord/{json,types,state}.go` and
`packages/chord/delta/{operations,diff,tracker,codec}.go`, plus focused normal
implementation tests. More idiomatic file splitting is allowed within these
owned directories. Existing `packages/chord/context/{context,value}.go` and
all their exported names stay compatible; no known change is required there.
Never modify permanent independent judges or unrelated packages to satisfy this
step. The port may use mature pure-Go dependencies, but this dependency slice
should normally need only the standard library. Builds and ordinary validation
run with `CGO_ENABLED=0`.

Codec interning occurs on a path's second use. Dictionary definitions and IDs
are local to one state stream. Same-path omission is scoped to one batch; a
batch's first operation always has an explicit path or an existing dictionary
reference. A replacement resets dictionary state so later recovery can start
with a fresh decoder. Unknown IDs, omitted first paths, invalid tuple grammar,
and unsafe dictionary paths return errors. Encoding/decoding is lossless over
decoded operations and does not mutate its input.

Candidate-owned normal tests should be added beside JSON/state/delta code for
all exported behavior. They must cover error/rollback paths, invalid ownership,
Unicode boundary behavior, no-op revisions, stalled observers and disposal.
The independent `portsmith_judge_*` tests remain read-only acceptance evidence;
they supplement implementation tests and do not claim exhaustive full-Chord
parity. The existing context implementation and its normal tests stay present.
