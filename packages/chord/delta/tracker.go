package delta

import (
	"errors"
	"sync"
)

// trackerStatus is the lifecycle state of one draft or prepared candidate.
type trackerStatus int

const (
	statusOpen trackerStatus = iota
	statusPrepared
	statusConsumed
	statusAborted
	statusStale
)

// ownerToken gives every Tracker a process-unique identity for ownership
// checks.
type ownerToken struct{}

type invalidatable interface {
	invalidate()
}

// Tracker owns one immutable JSON revision and hands out mutable drafts.
//
// Go cannot revoke an escaped map reference the way a JavaScript Proxy can. The
// explicit adaptation is to copy container data on preparation, to invalidate
// the change handle once it is settled, and to return independent copies from
// public value getters so retained drafts can never change committed revisions.
type Tracker struct {
	mu       sync.Mutex
	owner    *ownerToken
	value    any
	revision uint64
	cells    []invalidatable
}

// Change is one open mutable draft.
type Change struct {
	tracker      *Tracker
	draft        any
	base         any
	baseRevision uint64
	status       trackerStatus
	settled      bool
	prepared     *Prepared
}

func (c *Change) invalidate() {
	if c.status == statusOpen || c.status == statusPrepared {
		c.status = statusStale
	}
}

// Prepared is one validated candidate revision that may be adopted once.
type Prepared struct {
	tracker      *Tracker
	base         any
	value        any
	ops          []Op
	baseRevision uint64
	status       trackerStatus
}

func (p *Prepared) invalidate() {
	if p.status == statusPrepared {
		p.status = statusStale
	}
}

// Track takes immutable ownership of an alias-free strict-JSON object or array
// root.
func Track(initial any) (*Tracker, error) {
	switch initial.(type) {
	case map[string]any, []any:
	default:
		return nil, errors.New("delta: track requires a JSON object or array root")
	}
	detached, err := cloneJSON(initial)
	if err != nil {
		return nil, err
	}
	return &Tracker{owner: &ownerToken{}, value: detached}, nil
}

// Value returns an independent copy of the current revision.
func (t *Tracker) Value() any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return mustClone(t.value)
}

// Revision returns the number of adopted revisions.
func (t *Tracker) Revision() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.revision
}

// BeginChange opens a private mutable working copy of the current revision.
func (t *Tracker) BeginChange() (*Change, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	draft, err := cloneJSON(t.value)
	if err != nil {
		return nil, err
	}
	base, err := cloneJSON(t.value)
	if err != nil {
		return nil, err
	}
	change := &Change{tracker: t, draft: draft, base: base, baseRevision: t.revision, status: statusOpen}
	t.cells = append(t.cells, change)
	return change, nil
}

// Value returns the live mutable draft. The returned value is the mutation
// surface for this change and is only valid until the change is prepared or
// aborted.
func (c *Change) Value() (any, error) {
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	if c.settled || c.status != statusOpen {
		return nil, errors.New("delta: change has already been settled")
	}
	return c.draft, nil
}

// Prepare validates and detaches the draft, records its base revision, and
// leaves the tracker unchanged.
func (c *Change) Prepare() (*Prepared, error) {
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	if c.settled {
		return nil, errors.New("delta: change has already been settled")
	}
	if c.status != statusOpen || c.baseRevision != c.tracker.revision {
		c.status = statusStale
		c.settled = true
		return nil, errors.New("delta: change is stale")
	}
	if !IsJSONValue(c.draft) {
		c.status = statusAborted
		c.settled = true
		return nil, errors.New("delta: change draft is not strict JSON")
	}
	value, err := cloneJSON(c.draft)
	if err != nil {
		c.status = statusAborted
		c.settled = true
		return nil, err
	}
	base, err := cloneJSON(c.base)
	if err != nil {
		c.status = statusAborted
		c.settled = true
		return nil, err
	}
	ops, err := DiffRevisions(base, value)
	if err != nil {
		c.status = statusAborted
		c.settled = true
		return nil, err
	}
	prepared := &Prepared{
		tracker:      c.tracker,
		base:         base,
		value:        value,
		ops:          ops,
		baseRevision: c.baseRevision,
		status:       statusPrepared,
	}
	c.status = statusPrepared
	c.settled = true
	c.prepared = prepared
	c.tracker.cells = append(c.tracker.cells, prepared)
	return prepared, nil
}

// Abort discards the change. It is idempotent.
func (c *Change) Abort() {
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	if c.settled {
		if c.prepared != nil && c.prepared.status == statusPrepared {
			c.prepared.status = statusAborted
		}
		return
	}
	c.settled = true
	c.status = statusAborted
}

// PrepareReplace validates and detaches a replacement revision.
func (t *Tracker) PrepareReplace(value any) (*Prepared, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch value.(type) {
	case map[string]any, []any:
		if !IsJSONValue(value) {
			return nil, errors.New("delta: replacement is not strict JSON")
		}
	default:
		return nil, errors.New("delta: replacement must be a JSON object or array")
	}
	candidate, err := cloneJSON(value)
	if err != nil {
		return nil, err
	}
	base, err := cloneJSON(t.value)
	if err != nil {
		return nil, err
	}
	ops := []Op{}
	if !jsonEqual(base, candidate) {
		ops = []Op{{"r", candidate}}
	}
	prepared := &Prepared{
		tracker:      t,
		base:         base,
		value:        candidate,
		ops:          ops,
		baseRevision: t.revision,
		status:       statusPrepared,
	}
	t.cells = append(t.cells, prepared)
	return prepared, nil
}

// Adopt commits one prepared candidate. It requires the same owner, the current
// base revision, prepared status, and one-time consumption.
func (t *Tracker) Adopt(prepared *Prepared) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if prepared == nil {
		return errors.New("delta: nil prepared candidate")
	}
	if prepared.tracker != t {
		return errors.New("delta: prepared change belongs to a different tracker")
	}
	switch prepared.status {
	case statusConsumed:
		return errors.New("delta: prepared change has already been used")
	case statusAborted:
		return errors.New("delta: prepared change has been aborted")
	case statusStale:
		return errors.New("delta: prepared change is stale")
	case statusPrepared:
	default:
		return errors.New("delta: prepared change is not ready")
	}
	if prepared.baseRevision != t.revision {
		prepared.status = statusStale
		return errors.New("delta: prepared change is stale")
	}
	t.value = prepared.value
	prepared.status = statusConsumed
	t.revision++
	for _, cell := range t.cells {
		cell.invalidate()
	}
	return nil
}

// Base returns an independent copy of the candidate's base revision.
func (p *Prepared) Base() any {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	return mustClone(p.base)
}

// Value returns an independent copy of the candidate value.
func (p *Prepared) Value() any {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	return mustClone(p.value)
}

// Ops returns the operation batch that replays the base into the candidate.
func (p *Prepared) Ops() []Op {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	if len(p.ops) == 0 {
		return nil
	}
	out := make([]Op, len(p.ops))
	copy(out, p.ops)
	return out
}

// BaseRevision returns the tracker revision the candidate was prepared against.
func (p *Prepared) BaseRevision() uint64 {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	return p.baseRevision
}

// Abort discards the prepared candidate. It is idempotent.
func (p *Prepared) Abort() {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	if p.status == statusPrepared {
		p.status = statusAborted
	}
}

func mustClone(value any) any {
	cloned, err := cloneJSON(value)
	if err != nil {
		return value
	}
	return cloned
}
