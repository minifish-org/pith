package chord

import (
	"context"

	"github.com/minifish-org/pith/packages/chord/delta"
)

// JSONValue is the strict-JSON value union represented natively by any.
type JSONValue = any

// Op is one decoded delta operation tuple.
type Op = delta.Op

// Path is one decoded tuple path.
type Path = delta.Path

// StateSnapshot is an immutable value captured at the atomic attachment
// boundary together with its monotonic source cursor.
type StateSnapshot struct {
	Value  any
	Cursor uint64
}

// StateFrame is one immutable authoritative revision committed after the
// attachment snapshot. Source frames carry authoritative revisions: Chord
// publishes Value directly and never re-applies or re-diffs Ops.
type StateFrame struct {
	Value   any
	Cursor  uint64
	Ops     []delta.Op
	Context context.Context
}

// StateSource is an authoritative immutable revision source. Attach must
// synchronously and atomically capture one snapshot and register the returned
// attachment to buffer every later committed frame.
type StateSource interface {
	Attach() (StateAttachment, error)
}

// StateAttachment is a single-use source attachment that drains buffered frames
// in source commit order once activated. Disposal must be idempotent.
type StateAttachment interface {
	Snapshot() StateSnapshot
	Activate(func(StateFrame)) error
	Dispose()
}

// StateDelivery identifies one publication delivery to a subscriber.
type StateDelivery struct {
	Kind     string
	Sequence uint64
}

// StateListener observes hydrated and updated values. Each subscription
// serializes its callbacks independently.
type StateListener func(any, context.Context, StateDelivery) error

// StateOptions configures source-attached publication. OnError receives source
// contract and listener failures without throwing them into the source.
type StateOptions struct {
	OnError func(error)
}
