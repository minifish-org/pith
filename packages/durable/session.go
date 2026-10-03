package durable

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/chord/delta"
)

// CommitChange is one entry of a committed publication: either a raw record
// write (conversation, entry, task, submission) or one document change.
//
// For record writes Type is the storage write discriminator and Write carries
// the retained write. For document changes Type is "document" or
// "document.copy"; Record, ConversationID, Version, Value, Ops, and Source
// describe the committed incarnation.
type CommitChange struct {
	Type           string
	Write          *StorageWrite
	Record         *DocumentRecord
	ConversationID *ConversationID
	Version        *int
	Value          JsonObject
	Ops            []chord.Op
	Source         *DocumentCopySource
}

// CommitPublication is the ordered description of one committed storage batch.
type CommitPublication struct {
	Seq     Seq
	Changes []CommitChange
}

// WatchEnd reports why a watch or state stream terminated. Reason is one of
// "stopped", "retired", "cancelled", "session_closed", or "listener_error".
type WatchEnd struct {
	Reason string
	Err    error
}

// DocumentReader is the read-only document access surface.
type DocumentReader interface {
	Snapshot(context.Context, DocumentDefinition, DocumentAddress) (JsonObject, error)
	SnapshotAsOf(context.Context, DocumentDefinition, DocumentAddress, EntryID) (JsonObject, error)
}

// DocumentObserver is the document watch acquisition surface.
type DocumentObserver interface {
	WatchDoc(context.Context, DocumentDefinition, DocumentAddress) (*DocumentWatch, error)
}

// loadedDocument is one committed document incarnation owned by the Session
// tracker cache.
type loadedDocument struct {
	addressID string
	record    DocumentRecord
	// storedVersion is the persisted definition version; it is older than
	// valueVersion while the tracked value is migrated only in memory.
	storedVersion int
	// valueVersion is the definition version whose shape the tracked value has.
	valueVersion int
	// deltasSinceBase is the stored delta count after the newest base, advanced
	// by adoption so the next checkpoint predicate needs no read.
	deltasSinceBase int
	tracker         *delta.Tracker
}

// commitListener observes one committed publication on the mutation line.
type commitListener func(CommitPublication, context.Context)

// Session is the transactional kernel over one Storage backend. It owns the
// single mutation line, the loaded document tracker cache, and committed
// publication. Only committed state is observable.
//
// Every commit callback, preparation, storage settlement, adoption, and
// publication enqueue runs while the line is held; listeners run later.
type Session struct {
	storage Storage

	line sync.Mutex

	metaMu  sync.Mutex
	closing bool
	poison  error

	documents map[string]*loadedDocument

	subMu           sync.Mutex
	commitListeners map[uint64]commitListener
	closeListeners  map[uint64]func()
	nextListener    uint64

	closeOnce sync.Once
	closeErr  error

	// conversationCreated stages the documents of every newly created or forked
	// conversation inside its creating transaction. The plain Session stages
	// nothing; a harness stages its built-in documents.
	conversationCreated func(*Transaction, ConversationRecord) error
}

// TransactionScope sets the defaults a commit binds to: the conversation used
// by CreateTask when options omit one, and the task attributed to appended
// entries.
type TransactionScope struct {
	// ConversationID optionally selects the default conversation for
	// TaskOptions without an explicit conversation.
	ConversationID *ConversationID
	// TaskID optionally attributes appended entries to a task runtime commit.
	TaskID *TaskID
}

var (
	_ DocumentReader   = (*Session)(nil)
	_ DocumentObserver = (*Session)(nil)
)

// NewSession opens a Session kernel over one storage backend.
func NewSession(storage Storage) *Session {
	return &Session{
		storage:         storage,
		documents:       map[string]*loadedDocument{},
		commitListeners: map[uint64]commitListener{},
		closeListeners:  map[uint64]func(){},
	}
}

// Commit runs one serialized commit callback and adopts its staged writes.
//
// A callback error aborts every staged change and leaves storage untouched. A
// callback that stages no write emits no storage commit and no publication.
// After storage admission begins, caller cancellation no longer interrupts
// settlement: acknowledged commits remain committed.
func (s *Session) Commit(ctx context.Context, change func(Tx) error) error {
	return s.commitWith(ctx, TransactionScope{}, func(tx *Transaction) error { return change(tx) })
}

// CommitTransaction is the concrete-transaction form of Commit. It exposes the
// transaction type and scope used by a harness to run the reserved-ID root
// bootstrap and scheduling task replacement.
func (s *Session) CommitTransaction(ctx context.Context, scope TransactionScope, change func(*Transaction) error) error {
	return s.commitWith(ctx, scope, change)
}

// SetConversationCreated installs the hook run in every commit that creates or
// forks a conversation, after the conversation write is staged and before the
// transaction settles. The harness uses it to create the built-in pi.*
// documents and the conversation's agent.
func (s *Session) SetConversationCreated(fn func(*Transaction, ConversationRecord) error) {
	s.conversationCreated = fn
}

// ReadOnLine runs fn with a read-only transaction while holding the single
// mutation line, so a caller observes exactly one committed state. fn must not
// write; staged writes are discarded. It is the harness equivalent of the
// upstream SessionImpl.readOnLine.
func (s *Session) ReadOnLine(ctx context.Context, fn func(*Transaction) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.assertUsable(); err != nil {
		return err
	}
	s.line.Lock()
	defer s.line.Unlock()
	if err := s.assertHealthy(); err != nil {
		return err
	}
	tx := newTransaction(s, TransactionScope{}, ctx)
	defer tx.discard()
	return fn(tx)
}

func (s *Session) commitWith(ctx context.Context, scope TransactionScope, change func(*Transaction) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.assertUsable(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.line.Lock()
	defer s.line.Unlock()
	if err := s.assertHealthy(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tx := newTransaction(s, scope, ctx)
	if err := change(tx); err != nil {
		tx.settleFailure()
		return err
	}
	writes, err := tx.settleSuccess()
	if err != nil {
		return err
	}
	if len(writes) == 0 {
		tx.discard()
		return nil
	}
	seq, err := s.storage.Commit(context.WithoutCancel(ctx), writes)
	if err != nil {
		tx.discard()
		var rejected *StorageRejected
		if !errors.As(err, &rejected) {
			s.setPoison(err)
		}
		return err
	}
	changes, err := tx.adopt(seq)
	if err != nil {
		// Storage already committed; a failed adoption leaves memory behind
		// durable state, so the Session poisons.
		s.setPoison(err)
		return err
	}
	s.publish(seq, writes, changes, ctx)
	return nil
}

// Snapshot returns the current committed value of one logical address, or nil
// when the address is absent. It never creates a document.
func (s *Session) Snapshot(ctx context.Context, definition DocumentDefinition, address DocumentAddress) (JsonObject, error) {
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	s.line.Lock()
	defer s.line.Unlock()
	if err := s.assertHealthy(); err != nil {
		return nil, err
	}
	loaded, err := s.loadDocument(&definition, AddressID(address), address, ctx)
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, nil
	}
	if err := CheckRecordScope(&definition, loaded.record); err != nil {
		return nil, err
	}
	if err := CheckVersion(&definition, loaded.record.ID, loaded.record.Kind, loaded.storedVersion); err != nil {
		return nil, err
	}
	return trackerObject(loaded.tracker)
}

// SnapshotAsOf returns the final document state of the commit that persisted one
// visible entry, or nil when no document was alive at that point.
func (s *Session) SnapshotAsOf(ctx context.Context, definition DocumentDefinition, address DocumentAddress, at EntryID) (JsonObject, error) {
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	if address.Scope.Kind != ScopeConversation {
		return nil, errors.New("Session.SnapshotAsOf() requires a conversation document")
	}
	conversationID := address.Scope.ConversationID
	s.line.Lock()
	defer s.line.Unlock()
	if err := s.assertHealthy(); err != nil {
		return nil, err
	}
	storedEntry, err := s.storage.VisibleEntry(ctx, conversationID, at)
	if err != nil {
		return nil, err
	}
	if storedEntry == nil {
		return nil, fmt.Errorf("Entry %d is not visible from conversation %d", at, conversationID)
	}
	resolved := address
	resolved.Scope.ConversationID = storedEntry.Entry.ConversationID
	point := AtSeq(storedEntry.CommitSeq)
	record, err := s.storage.FindDocument(ctx, resolved, point)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	content, err := s.storage.Document(ctx, record.ID, point)
	if err != nil {
		return nil, err
	}
	if content == nil {
		return nil, fmt.Errorf("Historical document %d (%s) cannot be read", record.ID, record.Kind)
	}
	return MaterializeDocument(&definition, content.Record.Kind, content.Record.Scope, content.Record.History, content.Record.Fork, content.Record.ID, content.Version, content.Value)
}

// WatchDoc atomically captures the committed value of one document incarnation
// and registers for later commits. It never observes drafts and never creates a
// document. A nil *DocumentWatch means the address is absent.
func (s *Session) WatchDoc(ctx context.Context, definition DocumentDefinition, address DocumentAddress) (*DocumentWatch, error) {
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.line.Lock()
	if err := s.assertHealthy(); err != nil {
		s.line.Unlock()
		return nil, err
	}
	loaded, err := s.loadDocument(&definition, AddressID(address), address, ctx)
	if err != nil {
		s.line.Unlock()
		return nil, err
	}
	if loaded == nil {
		s.line.Unlock()
		return nil, nil
	}
	watch, err := s.attachWatch(&definition, loaded)
	s.line.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		watch.cancel()
		return nil, err
	}
	watch.observeContext(ctx)
	return watch, nil
}

// DocumentState atomically captures the committed value of one document
// incarnation as publication-only replicated state. It never creates a
// document. A nil *chord.AttachedState means the address is absent.
func (s *Session) DocumentState(ctx context.Context, definition DocumentDefinition, address DocumentAddress) (*chord.AttachedState, error) {
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	s.line.Lock()
	defer s.line.Unlock()
	if err := s.assertHealthy(); err != nil {
		return nil, err
	}
	loaded, err := s.loadDocument(&definition, AddressID(address), address, ctx)
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, nil
	}
	if err := CheckRecordScope(&definition, loaded.record); err != nil {
		return nil, err
	}
	if err := CheckVersion(&definition, loaded.record.ID, loaded.record.Kind, loaded.storedVersion); err != nil {
		return nil, err
	}
	value, err := trackerObject(loaded.tracker)
	if err != nil {
		return nil, err
	}
	source := newDocumentSource(value, nil)
	source.release = s.observeDocument(&definition, loaded, source, true)
	state, err := chord.AttachReplicatedState(source, chord.StateOptions{})
	if err != nil {
		source.closeSession()
		return nil, err
	}
	return state, nil
}

// SubscribeCommits registers a post-adoption publication listener. It must not
// block or call Session operations. The returned function unsubscribes.
func (s *Session) SubscribeCommits(listener func(CommitPublication)) func() {
	if listener == nil {
		return func() {}
	}
	if err := s.assertUsable(); err != nil {
		return func() {}
	}
	id := s.subscribeCommits(func(publication CommitPublication, _ context.Context) {
		listener(publication)
	})
	return func() { s.unsubscribeCommits(id) }
}

// SubscribeClose registers a synchronous listener called when Close begins. The
// returned function unsubscribes.
func (s *Session) SubscribeClose(listener func()) func() {
	if listener == nil {
		return func() {}
	}
	if err := s.assertUsable(); err != nil {
		return func() {}
	}
	id := s.subscribeClose(listener)
	return func() { s.unsubscribeClose(id) }
}

// Close seals admission immediately, terminates watches, joins admitted work,
// then closes storage. It is idempotent; a later close joins the same shutdown
// even after the caller's context is cancelled.
func (s *Session) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.closeOnce.Do(func() {
		s.metaMu.Lock()
		s.closing = true
		s.metaMu.Unlock()
		// Terminate observers before joining admitted commits; their detach only
		// touches the subscription set, never the mutation line.
		s.fireCloseListeners()
		s.line.Lock()
		s.subMu.Lock()
		s.commitListeners = map[uint64]commitListener{}
		s.closeListeners = map[uint64]func(){}
		s.subMu.Unlock()
		s.documents = map[string]*loadedDocument{}
		s.line.Unlock()
		s.closeErr = s.storage.Close(context.WithoutCancel(ctx))
	})
	return s.closeErr
}

// ─── Internal helpers ───────────────────────────────────────────────────────

func (s *Session) assertUsable() error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if s.closing {
		return errors.New("Session is closed")
	}
	if s.poison != nil {
		return fmt.Errorf("Session is poisoned by a failed commit after storage admission; reopen it: %w", s.poison)
	}
	return nil
}

func (s *Session) assertHealthy() error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	if s.poison != nil {
		return fmt.Errorf("Session is poisoned by a failed commit after storage admission; reopen it: %w", s.poison)
	}
	return nil
}

func (s *Session) setPoison(err error) {
	s.metaMu.Lock()
	if s.poison == nil {
		s.poison = err
	}
	s.metaMu.Unlock()
}

func (s *Session) subscribeCommits(listener commitListener) uint64 {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.nextListener++
	id := s.nextListener
	if s.commitListeners == nil {
		s.commitListeners = map[uint64]commitListener{}
	}
	s.commitListeners[id] = listener
	return id
}

func (s *Session) unsubscribeCommits(id uint64) {
	s.subMu.Lock()
	delete(s.commitListeners, id)
	s.subMu.Unlock()
}

func (s *Session) subscribeClose(listener func()) uint64 {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	s.nextListener++
	id := s.nextListener
	if s.closeListeners == nil {
		s.closeListeners = map[uint64]func(){}
	}
	s.closeListeners[id] = listener
	return id
}

func (s *Session) unsubscribeClose(id uint64) {
	s.subMu.Lock()
	delete(s.closeListeners, id)
	s.subMu.Unlock()
}

func (s *Session) fireCloseListeners() {
	s.subMu.Lock()
	listeners := make([]func(), 0, len(s.closeListeners))
	for _, listener := range s.closeListeners {
		listeners = append(listeners, listener)
	}
	s.subMu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (s *Session) publish(seq Seq, writes []StorageWrite, documentChanges []CommitChange, ctx context.Context) {
	s.subMu.Lock()
	if len(s.commitListeners) == 0 {
		s.subMu.Unlock()
		return
	}
	listeners := make([]commitListener, 0, len(s.commitListeners))
	for _, listener := range s.commitListeners {
		listeners = append(listeners, listener)
	}
	s.subMu.Unlock()
	changes := make([]CommitChange, 0, len(writes)+len(documentChanges))
	for index := range writes {
		switch writes[index].Type {
		case WriteConversation, WriteEntry, WriteTask, WriteSubmission:
			write := writes[index]
			changes = append(changes, CommitChange{Type: write.Type, Write: &write})
		}
	}
	changes = append(changes, documentChanges...)
	publication := CommitPublication{Seq: seq, Changes: changes}
	for _, listener := range listeners {
		listener(publication, ctx)
	}
}

func (s *Session) install(document *loadedDocument) {
	s.documents[document.addressID] = document
}

func (s *Session) evict(addressID string, recordID DocumentID) {
	if cached := s.documents[addressID]; cached != nil && cached.record.ID == recordID {
		delete(s.documents, addressID)
	}
}

// loadDocument returns the cached current incarnation, cold-loading and
// migrating it when necessary. The caller must hold the mutation line.
func (s *Session) loadDocument(definition *DocumentDefinition, addressID string, address DocumentAddress, ctx context.Context) (*loadedDocument, error) {
	cached := s.documents[addressID]
	if cached != nil && cached.valueVersion == definition.Version {
		return cached, nil
	}
	if cached != nil {
		delete(s.documents, addressID)
	}
	record, err := s.storage.FindDocument(ctx, address, CurrentDocument())
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	stored, err := s.storage.Document(ctx, record.ID, CurrentDocument())
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("Current document %d (%s) cannot be read", record.ID, record.Kind)
	}
	value, err := MaterializeDocument(definition, stored.Record.Kind, stored.Record.Scope, stored.Record.History, stored.Record.Fork, stored.Record.ID, stored.Version, stored.Value)
	if err != nil {
		return nil, err
	}
	tracker, err := delta.Track(map[string]any(value))
	if err != nil {
		return nil, err
	}
	loaded := &loadedDocument{
		addressID:       addressID,
		record:          stored.Record,
		storedVersion:   stored.Version,
		valueVersion:    definition.Version,
		deltasSinceBase: stored.DeltasSinceBase,
		tracker:         tracker,
	}
	s.documents[addressID] = loaded
	return loaded, nil
}

// attachWatch installs one document watch and its commit/close subscriptions.
// The caller must hold the mutation line.
func (s *Session) attachWatch(definition *DocumentDefinition, loaded *loadedDocument) (*DocumentWatch, error) {
	if err := CheckRecordScope(definition, loaded.record); err != nil {
		return nil, err
	}
	if err := CheckVersion(definition, loaded.record.ID, loaded.record.Kind, loaded.storedVersion); err != nil {
		return nil, err
	}
	value, err := trackerObject(loaded.tracker)
	if err != nil {
		return nil, err
	}
	watch := newDocumentWatch(value)
	watch.setDetach(s.observeDocument(definition, loaded, watch, false))
	return watch, nil
}

// sessionObserver receives committed document frames.
type sessionObserver interface {
	advance(value JsonObject, ops []chord.Op, ctx context.Context)
	closeSession()
}

// observeDocument subscribes one observer to an incarnation's committed changes
// and to session close. It returns the detach function. The caller must hold the
// mutation line.
func (s *Session) observeDocument(definition *DocumentDefinition, loaded *loadedDocument, observer sessionObserver, stateFrames bool) func() {
	observed := loaded.valueVersion
	commitID := s.subscribeCommits(func(publication CommitPublication, ctx context.Context) {
		for _, change := range publication.Changes {
			if change.Type != "document" || change.Record == nil || change.Record.ID != loaded.record.ID {
				continue
			}
			// A document state's frames carry no caller cancellation; a watch
			// observes its own cancellation.
			frameContext := ctx
			if stateFrames {
				frameContext = context.WithoutCancel(ctx)
			}
			ops := observedOperations(&observed, change)
			// A migration-only base changes nothing for an observer of the new
			// version.
			if len(ops) == 0 {
				continue
			}
			observer.advance(change.Value, ops, frameContext)
		}
	})
	closeID := s.subscribeClose(func() { observer.closeSession() })
	return func() {
		s.unsubscribeCommits(commitID)
		s.unsubscribeClose(closeID)
	}
}

// observedOperations returns the operations an observer applies for one
// committed change. An observer hydrated under another definition version holds
// a differently shaped value, so it receives the new value as a root
// replacement instead of operations for that shape.
func observedOperations(observed *int, change CommitChange) []chord.Op {
	if change.Value == nil {
		return retirementOperations
	}
	if change.Version != nil && *change.Version == *observed {
		return change.Ops
	}
	if change.Version != nil {
		*observed = *change.Version
	}
	return []chord.Op{{"r", change.Value}}
}

func trackerObject(tracker *delta.Tracker) (JsonObject, error) {
	value := tracker.Value()
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("document value is not a JSON object")
	}
	return JsonObject(object), nil
}
