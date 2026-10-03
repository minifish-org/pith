package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/chord/delta"
)

const internalScanPageSize = 256

// submissionChange is a staged settlement or the placement of a queued
// submission at its entry.
type submissionChange struct {
	id     SubmissionID
	change submissionChangeValue
}

type submissionChangeValue struct {
	placed bool
	entry  EntryID
	settle SubmissionSettlement
}

// taskWrite is one staged task creation or whole-record replacement.
type taskWrite struct {
	kind   string // taskWriteCreate or taskWriteReplace
	record *TaskRecord
}

const (
	taskWriteCreate  = "create"
	taskWriteReplace = "replace"

	targetLoaded     = "loaded"
	targetCreated    = "created"
	targetForkCopy   = "fork-copy"
	targetRetireOnly = "retire-only"
)

// transactionTask is the committed and candidate state for one task touched by
// this transaction.
type transactionTask struct {
	committedRead             *TaskRecord
	committedLoad             bool
	write                     *taskWrite
	publicationConversationID *ConversationID
}

// documentTarget records the storage/cache provenance of one staged document
// incarnation.
type documentTarget struct {
	kind         string
	loaded       *loadedDocument
	record       DocumentCreate
	version      int
	tracker      *delta.Tracker
	source       DocumentCopySource
	retireRecord DocumentRecord
}

// documentEntry is one document incarnation acquired, created, or retired by
// this transaction.
type documentEntry struct {
	addressID string
	address   DocumentAddress
	// definition is absent for definition-free fork copies and retirement
	// entries discovered by a terminal-task scan.
	definition *DocumentDefinition
	draft      *DocumentDraft
	draftErr   error
	target     *documentTarget
	change     *delta.Change
	prepared   *delta.Prepared

	retireOnCommit bool
}

type planChange struct {
	tracker    *delta.Tracker
	prepared   *delta.Prepared
	version    int
	loaded     *loadedDocument
	definition *DocumentDefinition
}

// documentPlan is what one staged incarnation writes and publishes, decided
// once before storage admission so adoption only applies it.
type documentPlan struct {
	addressID string
	create    *DocumentCreate
	record    *DocumentRecord
	retire    bool
	content   *StorageWrite
	change    *planChange

	conversationID *ConversationID
}

// Transaction is the transaction surface of one Session commit callback. All
// asynchronous upstream operations are represented as blocking Go calls.
type Transaction struct {
	session *Session
	scope   TransactionScope
	ctx     context.Context

	sealed        bool
	hasTableWrite bool

	writes                    []StorageWrite
	createdConversationIDs    map[ConversationID]struct{}
	forkSourceConversationIDs map[ConversationID]struct{}
	forkSourceDocumentIDs     map[DocumentID]struct{}

	tasksByID map[TaskID]*transactionTask

	submissions       map[SubmissionID]*SubmissionRecord
	submissionChanges []submissionChange

	plans                   []*documentPlan
	documents               []*documentEntry
	latestDocumentByAddress map[string]*documentEntry
}

func newTransaction(session *Session, scope TransactionScope, ctx context.Context) *Transaction {
	return &Transaction{
		session:                   session,
		scope:                     scope,
		ctx:                       ctx,
		createdConversationIDs:    map[ConversationID]struct{}{},
		forkSourceConversationIDs: map[ConversationID]struct{}{},
		forkSourceDocumentIDs:     map[DocumentID]struct{}{},
		tasksByID:                 map[TaskID]*transactionTask{},
		submissions:               map[SubmissionID]*SubmissionRecord{},
		latestDocumentByAddress:   map[string]*documentEntry{},
	}
}

// ─── Table reads ────────────────────────────────────────────────────────────

// Context returns the context the transaction runs under.
func (tx *Transaction) Context() context.Context { return tx.ctx }

// Conversation reads one committed conversation.
func (tx *Transaction) Conversation(ctx context.Context, id ConversationID) (*ConversationRecord, error) {
	if err := tx.beginRead("conversation"); err != nil {
		return nil, err
	}
	return tx.session.storage.Conversation(ctx, id)
}

// Entry reads one committed entry by global ID.
func (tx *Transaction) Entry(ctx context.Context, id EntryID) (*EntryRecord, error) {
	if err := tx.beginRead("entry"); err != nil {
		return nil, err
	}
	stored, err := tx.session.storage.Entry(ctx, id)
	if err != nil || stored == nil {
		return nil, err
	}
	return &stored.Entry, nil
}

// Task reads the latest committed record for one task.
func (tx *Transaction) Task(ctx context.Context, id TaskID) (*TaskRecord, error) {
	if err := tx.beginRead("task"); err != nil {
		return nil, err
	}
	return tx.committedTask(ctx, id)
}

// ScanConversations scans committed conversations in ascending ID order.
func (tx *Transaction) ScanConversations(ctx context.Context, query ConversationQuery, limit int, cursor Cursor) (Page[ConversationRecord], error) {
	if err := tx.beginRead("scanConversations"); err != nil {
		return Page[ConversationRecord]{}, err
	}
	return tx.session.storage.ScanConversations(ctx, query, limit, cursor)
}

// ScanEntries scans one conversation's visible history newest-first.
func (tx *Transaction) ScanEntries(ctx context.Context, query EntryQuery, limit int, cursor Cursor) (Page[EntryRecord], error) {
	if err := tx.beginRead("scanEntries"); err != nil {
		return Page[EntryRecord]{}, err
	}
	return tx.session.storage.ScanEntries(ctx, query, limit, cursor)
}

// LatestHeadMarker returns the newest visible entry of one conversation that
// carries a head marker.
func (tx *Transaction) LatestHeadMarker(ctx context.Context, conversationID ConversationID) (*EntryRecord, error) {
	if err := tx.beginRead("latestHeadMarker"); err != nil {
		return nil, err
	}
	return tx.session.storage.FindLatestHeadMarker(ctx, conversationID, nil)
}

// ScanTasks scans committed task records matching every filter.
func (tx *Transaction) ScanTasks(ctx context.Context, query TaskQuery, limit int, cursor Cursor) (Page[TaskRecord], error) {
	if err := tx.beginRead("scanTasks"); err != nil {
		return Page[TaskRecord]{}, err
	}
	return tx.session.storage.ScanTasks(ctx, query, limit, cursor)
}

// SubmissionByRequest finds a committed submission by its conversation-scoped
// request ID.
func (tx *Transaction) SubmissionByRequest(ctx context.Context, conversationID ConversationID, requestID string) (*SubmissionRecord, error) {
	if err := tx.beginRead("submissionByRequest"); err != nil {
		return nil, err
	}
	return tx.session.storage.SubmissionByRequest(ctx, conversationID, requestID)
}

// ─── Table writes ───────────────────────────────────────────────────────────

// CreateConversation creates a conversation with explicitly selected ownership.
func (tx *Transaction) CreateConversation(ctx context.Context, ownership ConversationOwnership) (*ConversationRecord, error) {
	if err := tx.beginWrite(); err != nil {
		return nil, err
	}
	return tx.stageConversation(ctx, nil, ownership, nil)
}

// CreateRootConversation is the exported bootstrap path for the reserved root
// identity. It is used by the harness to lazily create conversation 1.
func (tx *Transaction) CreateRootConversation(ctx context.Context) (*ConversationRecord, error) {
	return tx.createRootConversation(ctx)
}

// createRootConversation is the internal final-form bootstrap path for the
// reserved root identity.
func (tx *Transaction) createRootConversation(ctx context.Context) (*ConversationRecord, error) {
	if err := tx.beginWrite(); err != nil {
		return nil, err
	}
	reserved := RootConversationID
	return tx.stageConversation(ctx, nil, ConversationOwnership{Kind: "ownerless"}, &reserved)
}

// ForkConversation creates a history fork at one concrete visible entry with
// explicitly selected ownership.
func (tx *Transaction) ForkConversation(ctx context.Context, parent ConversationID, at EntryID, ownership ConversationOwnership) (*ConversationRecord, error) {
	if err := tx.beginWrite(); err != nil {
		return nil, err
	}
	return tx.stageConversation(ctx, &ConversationParent{ConversationID: parent, At: at}, ownership, nil)
}

func (tx *Transaction) stageConversation(ctx context.Context, parent *ConversationParent, ownership ConversationOwnership, reservedID *ConversationID) (*ConversationRecord, error) {
	var ownerTaskID TaskID
	hasOwner := ownership.Kind == "task"
	if hasOwner {
		ownerTaskID = ownership.TaskID
	}
	var id ConversationID
	if reservedID != nil {
		id = *reservedID
	} else {
		minted, err := tx.session.storage.MintID(ctx)
		if err != nil {
			return nil, err
		}
		id = ConversationID(minted)
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	var owner *ConversationOwner
	if hasOwner {
		task, err := tx.currentTask(ctx, ownerTaskID)
		if err != nil {
			return nil, err
		}
		if err := tx.assertOpen(); err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("Conversation owner task %d does not exist", ownerTaskID)
		}
		owner = &ConversationOwner{ConversationID: task.ConversationID, TaskID: ownerTaskID}
	}
	record := &ConversationRecord{ID: id, Parent: parent, Owner: owner}
	if parent != nil {
		copies, err := prepareForkDocumentCopies(tx.session.storage, parent.ConversationID, parent.At, id, ctx)
		if err != nil {
			return nil, err
		}
		if err := tx.assertOpen(); err != nil {
			return nil, err
		}
		for _, copy := range copies {
			tx.forkSourceDocumentIDs[copy.Source.ID] = struct{}{}
			address := DocumentAddress{Kind: copy.Record.Kind, Scope: copy.Record.Scope, Key: copy.Record.Key}
			entry := &documentEntry{
				addressID: AddressID(address),
				address:   address,
				target:    &documentTarget{kind: targetForkCopy, record: copy.Record, source: copy.Source},
			}
			tx.documents = append(tx.documents, entry)
			tx.latestDocumentByAddress[entry.addressID] = entry
		}
		tx.forkSourceConversationIDs[parent.ConversationID] = struct{}{}
	}
	tx.createdConversationIDs[id] = struct{}{}
	tx.writes = append(tx.writes, StorageWrite{Type: WriteConversation, Conversation: record})
	if tx.session.conversationCreated != nil {
		if err := tx.session.conversationCreated(tx, *record); err != nil {
			return nil, err
		}
		if err := tx.assertOpen(); err != nil {
			return nil, err
		}
	}
	return record, nil
}

// AppendEntry appends one immutable entry and returns the stored record.
func (tx *Transaction) AppendEntry(ctx context.Context, conversationID ConversationID, value EntryDraft) (*EntryRecord, error) {
	if err := tx.beginWrite(); err != nil {
		return nil, err
	}
	return tx.appendEntry(ctx, conversationID, value)
}

func (tx *Transaction) appendEntry(ctx context.Context, conversationID ConversationID, value EntryDraft) (*EntryRecord, error) {
	if err := tx.requireConversation(ctx, conversationID); err != nil {
		return nil, err
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	minted, err := tx.session.storage.MintID(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	id := EntryID(minted)
	record := &EntryRecord{
		ID:             id,
		ConversationID: conversationID,
		Kind:           value.Kind,
		Model:          value.Model,
		Data:           value.Data,
		Head:           value.Head,
		Edits:          value.Edits,
	}
	if value.HeadSelf {
		self := id
		record.Head = &self
	}
	if tx.scope.TaskID != nil {
		byTask := *tx.scope.TaskID
		record.ByTaskID = &byTask
	}
	tx.writes = append(tx.writes, StorageWrite{Type: WriteEntry, Entry: record})
	return record, nil
}

// CreateTask creates a task of the supplied definition and input.
func (tx *Transaction) CreateTask(ctx context.Context, definition TaskDefinition, input json.RawMessage, options TaskOptions) (TaskID, error) {
	if err := tx.beginWrite(); err != nil {
		return 0, err
	}
	ownership := options.Ownership
	hasOwner := ownership.Kind == "task"
	var owner *TaskRecord
	if hasOwner {
		current, err := tx.currentTask(ctx, ownership.TaskID)
		if err != nil {
			return 0, err
		}
		if err := tx.assertOpen(); err != nil {
			return 0, err
		}
		if current == nil {
			return 0, fmt.Errorf("Task owner %d does not exist", ownership.TaskID)
		}
		if options.Background {
			return 0, errors.New("A child task cannot be background")
		}
		if options.ConversationID != 0 && options.ConversationID != current.ConversationID {
			return 0, fmt.Errorf("A child task lives in its owner's conversation %d", current.ConversationID)
		}
		owner = current
	}
	var conversationID ConversationID
	switch {
	case owner != nil:
		conversationID = owner.ConversationID
	case options.ConversationID != 0:
		conversationID = options.ConversationID
	case tx.scope.ConversationID != nil:
		conversationID = *tx.scope.ConversationID
	default:
		return 0, errors.New("Tx.CreateTask() requires options.conversationId")
	}
	if err := tx.requireConversation(ctx, conversationID); err != nil {
		return 0, err
	}
	if err := tx.assertOpen(); err != nil {
		return 0, err
	}
	var checkpoint json.RawMessage
	if definition.Initial != nil {
		value, err := definition.Initial(cloneRaw(input))
		if err != nil {
			return 0, err
		}
		checkpoint = value
	}
	minted, err := tx.session.storage.MintID(ctx)
	if err != nil {
		return 0, err
	}
	if err := tx.assertOpen(); err != nil {
		return 0, err
	}
	id := TaskID(minted)
	record := &TaskRecord{
		ID:             id,
		ConversationID: conversationID,
		Kind:           definition.Name,
		Version:        definition.Version,
		Input:          cloneRaw(input),
		Background:     options.Background,
		State:          TaskState{Status: TaskStatusPending, Checkpoint: cloneRaw(checkpoint)},
	}
	if owner != nil {
		ownerID := owner.ID
		record.Owner = &ownerID
	}
	tx.tasksByID[id] = &transactionTask{write: &taskWrite{kind: taskWriteCreate, record: record}}
	return id, nil
}

// CreateSubmission creates a raw submission record with a fresh ID; no
// admission rules apply.
func (tx *Transaction) CreateSubmission(ctx context.Context, create SubmissionCreate) (*SubmissionRecord, error) {
	if err := tx.beginWrite(); err != nil {
		return nil, err
	}
	if err := tx.requireConversation(ctx, create.ConversationID); err != nil {
		return nil, err
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	minted, err := tx.session.storage.MintID(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	record := &SubmissionRecord{
		ID:             SubmissionID(minted),
		ConversationID: create.ConversationID,
		RequestID:      cloneString(create.RequestID),
		Type:           create.Type,
		Status:         create.Status,
		Entry:          cloneID(create.Entry),
		Answer:         cloneID(create.Answer),
		Reason:         create.Reason,
		Detail:         cloneRaw(create.Detail),
	}
	tx.submissions[record.ID] = record
	return record, nil
}

// SettleSubmission settles a queued or placed submission.
func (tx *Transaction) SettleSubmission(id SubmissionID, settlement SubmissionSettlement) error {
	if err := tx.assertOpen(); err != nil {
		return err
	}
	tx.hasTableWrite = true
	tx.submissionChanges = append(tx.submissionChanges, submissionChange{id: id, change: submissionChangeValue{settle: cloneSettlement(settlement)}})
	return nil
}

// PlaceSubmission places a queued submission at an entry.
func (tx *Transaction) PlaceSubmission(id SubmissionID, entry EntryID) error {
	if err := tx.assertOpen(); err != nil {
		return err
	}
	tx.hasTableWrite = true
	tx.submissionChanges = append(tx.submissionChanges, submissionChange{id: id, change: submissionChangeValue{placed: true, entry: entry}})
	return nil
}

// setTask replaces one task record completely. Tasks change their own state
// through their runtime.
func (tx *Transaction) setTask(value TaskRecord) error {
	if err := tx.assertOpen(); err != nil {
		return err
	}
	tx.hasTableWrite = true
	entry := tx.taskEntry(value.ID)
	if entry.write != nil && entry.write.record.State.Status == TaskStatusTerminal {
		return fmt.Errorf("Task %d already has a terminal candidate", value.ID)
	}
	if entry.write != nil && entry.write.record.ConversationID != value.ConversationID {
		return fmt.Errorf("Task %d cannot change conversations", value.ID)
	}
	cloned := cloneTaskRecord(value)
	kind := taskWriteReplace
	if entry.write != nil && entry.write.kind == taskWriteCreate {
		kind = taskWriteCreate
	}
	entry.write = &taskWrite{kind: kind, record: &cloned}
	return nil
}

// StagedTasks returns the candidate records of the tasks this transaction
// created or replaced so far. It is the exported form of stagedTasks used by
// the harness scheduler to build an ownership overlay.
func (tx *Transaction) StagedTasks() []*TaskRecord { return tx.stagedTasks() }

// StagedConversations returns the conversations this transaction created or
// forked so far, in creation order.
func (tx *Transaction) StagedConversations() []*ConversationRecord { return tx.stagedConversations() }

// SetTask is the exported form of setTask: it replaces one task record
// completely. The harness scheduler uses it to own task state transitions.
func (tx *Transaction) SetTask(value TaskRecord) error { return tx.setTask(value) }

// stagedTasks returns the candidate records of the tasks this transaction
// created or replaced so far.
func (tx *Transaction) stagedTasks() []*TaskRecord {
	records := []*TaskRecord{}
	for _, task := range tx.tasksByID {
		if task.write != nil {
			records = append(records, task.write.record)
		}
	}
	return records
}

// stagedConversations returns the conversations this transaction created or
// forked so far.
func (tx *Transaction) stagedConversations() []*ConversationRecord {
	records := []*ConversationRecord{}
	for index := range tx.writes {
		if tx.writes[index].Type == WriteConversation {
			records = append(records, tx.writes[index].Conversation)
		}
	}
	return records
}

// ─── Documents ──────────────────────────────────────────────────────────────

// Doc returns one private mutable document draft, creating the incarnation
// lazily when the logical address is absent.
func (tx *Transaction) Doc(ctx context.Context, definition DocumentDefinition, address DocumentAddress, seed json.RawMessage) (*DocumentDraft, error) {
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	addressID := AddressID(address)
	if err := tx.assertTaskDocumentsOpen(address); err != nil {
		return nil, err
	}
	latest := tx.latestDocumentByAddress[addressID]
	if latest != nil && !latest.retireOnCommit {
		if latest.draft != nil || latest.draftErr != nil {
			return latest.draft, latest.draftErr
		}
		if latest.target != nil && latest.target.kind == targetForkCopy {
			latest.draft, latest.draftErr = tx.acquireForkCopy(ctx, latest, &definition, latest.target)
			return latest.draft, latest.draftErr
		}
	}
	var seedValue json.RawMessage
	if definition.Family {
		seedValue = cloneRaw(seed)
	}
	entry := &documentEntry{addressID: addressID, address: address, definition: &definition}
	tx.documents = append(tx.documents, entry)
	tx.latestDocumentByAddress[addressID] = entry
	skipLoad := latest != nil && latest.retireOnCommit
	entry.draft, entry.draftErr = tx.acquire(ctx, entry, seedValue, skipLoad)
	return entry.draft, entry.draftErr
}

// RetireDoc retires the incarnation at one logical address.
func (tx *Transaction) RetireDoc(ctx context.Context, definition DocumentDefinition, address DocumentAddress) error {
	if err := tx.assertOpen(); err != nil {
		return err
	}
	addressID := AddressID(address)
	latest := tx.latestDocumentByAddress[addressID]
	if latest != nil && latest.retireOnCommit {
		return nil
	}
	if latest != nil && latest.target != nil && latest.target.kind == targetForkCopy {
		if err := CheckScope(&definition, latest.target.record); err != nil {
			return err
		}
		latest.retireOnCommit = true
		return nil
	}
	if latest != nil && (latest.draft != nil || latest.draftErr != nil) {
		latest.retireOnCommit = true
		return latest.draftErr
	}
	entry := &documentEntry{addressID: addressID, address: address, definition: &definition, retireOnCommit: true}
	tx.documents = append(tx.documents, entry)
	tx.latestDocumentByAddress[addressID] = entry
	return tx.findRetirement(ctx, entry)
}

func (tx *Transaction) acquire(ctx context.Context, entry *documentEntry, seed json.RawMessage, skipLoad bool) (*DocumentDraft, error) {
	definition := entry.definition
	var loaded *loadedDocument
	if !skipLoad {
		var err error
		loaded, err = tx.session.loadDocument(definition, entry.addressID, entry.address, ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	if loaded != nil {
		if err := CheckRecordScope(definition, loaded.record); err != nil {
			return nil, err
		}
		if err := CheckVersion(definition, loaded.record.ID, loaded.record.Kind, loaded.storedVersion); err != nil {
			return nil, err
		}
		entry.target = &documentTarget{kind: targetLoaded, loaded: loaded}
		change, err := loaded.tracker.BeginChange()
		if err != nil {
			return nil, err
		}
		entry.change = change
		return newDocumentDraft(loaded.tracker, change), nil
	}
	scope := entry.address.Scope
	if scope.Kind == ScopeConversation {
		if err := tx.requireConversation(ctx, scope.ConversationID); err != nil {
			return nil, err
		}
	}
	if scope.Kind == ScopeTask {
		task, err := tx.currentTask(ctx, scope.TaskID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("Task %d does not exist", scope.TaskID)
		}
		if task.State.Status == TaskStatusTerminal {
			return nil, fmt.Errorf("Task %d is terminal", scope.TaskID)
		}
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	if definition.Initial == nil {
		return nil, fmt.Errorf("Document %s has no initial value", definition.Kind)
	}
	var value JsonObject
	var err error
	if definition.Family {
		value, err = definition.Initial(seed)
	} else {
		value, err = definition.Initial(nil)
	}
	if err != nil {
		return nil, err
	}
	detached, err := chord.CopyJSON(map[string]any(value))
	if err != nil {
		return nil, err
	}
	object, ok := detached.(map[string]any)
	if !ok {
		return nil, errors.New("document initial value is not a JSON object")
	}
	minted, err := tx.session.storage.MintID(ctx)
	if err != nil {
		return nil, err
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	tracker, err := delta.Track(object)
	if err != nil {
		return nil, err
	}
	entry.target = &documentTarget{kind: targetCreated, record: DocumentCreateFor(definition, entry.address, DocumentID(minted)), version: definition.Version, tracker: tracker}
	change, err := tracker.BeginChange()
	if err != nil {
		return nil, err
	}
	entry.change = change
	return newDocumentDraft(tracker, change), nil
}

func (tx *Transaction) acquireForkCopy(ctx context.Context, entry *documentEntry, definition *DocumentDefinition, target *documentTarget) (*DocumentDraft, error) {
	stored, err := tx.session.storage.Document(ctx, target.source.ID, target.source.At)
	if err != nil {
		return nil, err
	}
	if err := tx.assertOpen(); err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, fmt.Errorf("Fork source document %d cannot be read", target.source.ID)
	}
	if stored.Record.Scope.Kind != ScopeConversation ||
		stored.Record.Kind != target.record.Kind ||
		!stringPtrEqual(stored.Record.Key, target.record.Key) ||
		stored.Record.History != target.record.History ||
		stored.Record.Fork != target.record.Fork {
		return nil, fmt.Errorf("Fork source document %d does not match the copied record", target.source.ID)
	}
	value, err := MaterializeDocument(definition, target.record.Kind, target.record.Scope, target.record.History, target.record.Fork, target.record.ID, stored.Version, stored.Value)
	if err != nil {
		return nil, err
	}
	tracker, err := delta.Track(map[string]any(value))
	if err != nil {
		return nil, err
	}
	entry.definition = definition
	entry.target = &documentTarget{kind: targetCreated, record: target.record, version: definition.Version, tracker: tracker}
	change, err := tracker.BeginChange()
	if err != nil {
		return nil, err
	}
	entry.change = change
	return newDocumentDraft(tracker, change), nil
}

func (tx *Transaction) findRetirement(ctx context.Context, entry *documentEntry) error {
	var record *DocumentRecord
	if cached := tx.session.documents[entry.addressID]; cached != nil {
		clone := cached.record
		record = &clone
	} else {
		found, err := tx.session.storage.FindDocument(ctx, entry.address, CurrentDocument())
		if err != nil {
			return err
		}
		record = found
	}
	if err := tx.assertOpen(); err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if err := CheckRecordScope(entry.definition, *record); err != nil {
		return err
	}
	entry.target = &documentTarget{kind: targetRetireOnly, retireRecord: *record}
	return nil
}

// ─── Settlement ─────────────────────────────────────────────────────────────

// settleFailure seals after callback failure and aborts every staged change.
func (tx *Transaction) settleFailure() {
	tx.sealed = true
	tx.abortChanges()
}

// settleSuccess seals after callback success, prepares every open change, and
// assembles the atomic batch. Any failure aborts every change before storage
// admission.
func (tx *Transaction) settleSuccess() ([]StorageWrite, error) {
	tx.sealed = true
	for _, document := range tx.documents {
		if document.change == nil {
			continue
		}
		prepared, err := document.change.Prepare()
		if err != nil {
			tx.abortChanges()
			return nil, err
		}
		document.prepared = prepared
	}
	writes, err := tx.assemble()
	if err != nil {
		tx.abortChanges()
		return nil, err
	}
	return writes, nil
}

// discard aborts every prepared change when no write is required or after a
// storage failure.
func (tx *Transaction) discard() {
	tx.abortChanges()
}

// adopt applies every prepared change by pointer swap after storage success and
// describes the publication.
func (tx *Transaction) adopt(seq Seq) ([]CommitChange, error) {
	publications := []CommitChange{}
	for _, plan := range tx.plans {
		committed := plan.record != nil
		var record DocumentRecord
		if committed {
			record = *plan.record
		} else {
			record = documentRecordFromCreate(*plan.create, seq)
		}
		if plan.retire {
			retired := seq
			record.RetiredAt = &retired
		}
		change := plan.change
		if change != nil {
			// A new incarnation is adopted unless it retires in the same commit;
			// a loaded one only when it changed.
			if change.loaded == nil {
				if !plan.retire {
					if err := change.tracker.Adopt(change.prepared); err != nil {
						return nil, err
					}
				} else {
					change.prepared.Abort()
				}
			} else {
				if len(change.prepared.Ops()) > 0 {
					if err := change.tracker.Adopt(change.prepared); err != nil {
						return nil, err
					}
				} else {
					change.prepared.Abort()
				}
			}
			if change.loaded != nil {
				if change.loaded.storedVersion < change.version {
					change.loaded.storedVersion = change.version
				}
				if plan.content != nil && plan.content.Type == WriteDocumentChange {
					if plan.content.Content.Kind == ContentBase {
						change.loaded.deltasSinceBase = 0
					} else {
						change.loaded.deltasSinceBase++
					}
				}
			} else if !plan.retire {
				tx.session.install(&loadedDocument{
					addressID:       plan.addressID,
					record:          record,
					storedVersion:   change.version,
					valueVersion:    change.version,
					deltasSinceBase: 0,
					tracker:         change.tracker,
				})
			}
		}
		if plan.retire {
			if committed {
				tx.session.evict(plan.addressID, record.ID)
			}
			publications = append(publications, CommitChange{Type: "document", Record: &record})
			continue
		}
		if plan.content != nil && plan.content.Type == WriteDocumentCopy {
			source := *plan.content.Source
			publications = append(publications, CommitChange{Type: "document.copy", Record: &record, ConversationID: plan.conversationID, Source: &source})
			continue
		}
		if change != nil && publishes(plan) {
			ops := []chord.Op{}
			if change.loaded != nil {
				ops = change.prepared.Ops()
			}
			value, err := preparedObject(change.prepared)
			if err != nil {
				return nil, err
			}
			version := change.version
			publications = append(publications, CommitChange{
				Type:           "document",
				Record:         &record,
				ConversationID: plan.conversationID,
				Version:        &version,
				Value:          value,
				Ops:            ops,
			})
		}
	}
	return publications, nil
}

func (tx *Transaction) assemble() ([]StorageWrite, error) {
	session := tx.session
	plans := tx.plans
	for _, document := range tx.documents {
		plan, err := tx.planDocument(document)
		if err != nil {
			return nil, err
		}
		if plan != nil {
			plans = append(plans, plan)
		}
	}
	tx.plans = plans
	if err := tx.rejectForkSourceWrites(plans); err != nil {
		return nil, err
	}
	if err := tx.validateOwners(); err != nil {
		return nil, err
	}
	for id, task := range tx.tasksByID {
		if task.write == nil || task.write.kind != taskWriteReplace {
			continue
		}
		committed, err := tx.committedTask(tx.ctx, id)
		if err != nil {
			return nil, err
		}
		if committed == nil {
			return nil, fmt.Errorf("Task %d does not exist", id)
		}
		if committed.State.Status == TaskStatusTerminal {
			return nil, fmt.Errorf("Task %d is already terminal", id)
		}
		if committed.ConversationID != task.write.record.ConversationID {
			return nil, fmt.Errorf("Task %d cannot change conversations", id)
		}
	}
	// Terminal settlement retires every task document, including ones created by
	// this transaction.
	terminalTaskIDs := map[TaskID]struct{}{}
	for _, task := range tx.tasksByID {
		if task.write != nil && task.write.record.State.Status == TaskStatusTerminal {
			terminalTaskIDs[task.write.record.ID] = struct{}{}
		}
	}
	if len(terminalTaskIDs) > 0 {
		retiring := map[DocumentID]struct{}{}
		for _, plan := range plans {
			scope := planScope(plan)
			if scope.Kind != ScopeTask {
				continue
			}
			if _, ok := terminalTaskIDs[scope.TaskID]; !ok {
				continue
			}
			plan.retire = true
			retiring[planID(plan)] = struct{}{}
		}
		for taskID := range terminalTaskIDs {
			if task := tx.tasksByID[taskID]; task != nil && task.write != nil && task.write.kind == taskWriteCreate {
				continue
			}
			var cursor Cursor
			for {
				page, err := session.storage.ScanDocuments(tx.ctx, DocumentQuery{Scope: DocumentScope{Kind: ScopeTask, TaskID: taskID}, At: CurrentDocument()}, internalScanPageSize, cursor)
				if err != nil {
					return nil, err
				}
				for index := range page.Items {
					record := page.Items[index]
					if _, ok := retiring[record.ID]; ok {
						continue
					}
					address := DocumentAddress{Kind: record.Kind, Scope: record.Scope, Key: record.Key}
					plans = append(plans, &documentPlan{addressID: AddressID(address), record: &record, retire: true})
					retiring[record.ID] = struct{}{}
				}
				cursor = page.Next
				if cursor == nil {
					break
				}
			}
		}
		tx.plans = plans
	}
	// Resolve publication ownership before storage admission so adoption remains
	// synchronous.
	for _, plan := range plans {
		if !publishes(plan) {
			continue
		}
		scope := planScope(plan)
		if scope.Kind == ScopeConversation {
			conversationID := scope.ConversationID
			plan.conversationID = &conversationID
			continue
		}
		if scope.Kind != ScopeTask {
			continue
		}
		task := tx.taskEntry(scope.TaskID)
		if task.publicationConversationID == nil {
			current, err := tx.currentTask(tx.ctx, scope.TaskID)
			if err != nil {
				return nil, err
			}
			if current != nil {
				conversationID := current.ConversationID
				task.publicationConversationID = &conversationID
			}
		}
		plan.conversationID = task.publicationConversationID
	}
	for _, change := range tx.submissionChanges {
		current := tx.submissions[change.id]
		if current == nil {
			found, err := session.storage.Submission(tx.ctx, change.id)
			if err != nil {
				return nil, err
			}
			current = found
		}
		if current == nil {
			return nil, fmt.Errorf("Submission %d does not exist", change.id)
		}
		next, err := applySubmissionChange(current, change.change)
		if err != nil {
			return nil, err
		}
		if next != current {
			tx.submissions[change.id] = next
		}
	}
	writes := tx.writes
	for _, value := range tx.submissions {
		writes = append(writes, StorageWrite{Type: WriteSubmission, Submission: value})
	}
	for _, task := range tx.tasksByID {
		if task.write != nil {
			writes = append(writes, StorageWrite{Type: WriteTask, Task: task.write.record})
		}
	}
	for _, plan := range plans {
		// Checkpoint predicates run last, after every validation, and exactly
		// once after Chord preparation.
		if plan.content != nil && plan.content.Type == WriteDocumentChange && plan.content.Content.Kind == ContentDelta && plan.change != nil && plan.change.loaded != nil {
			definition := plan.change.definition
			if definition != nil && definition.CheckpointWhen != nil {
				value, err := preparedObject(plan.change.prepared)
				if err != nil {
					return nil, err
				}
				info := CheckpointInfo{DeltasSinceBase: plan.change.loaded.deltasSinceBase}
				ok, err := definition.CheckpointWhen(value, plan.change.prepared.Ops(), info)
				if err != nil {
					return nil, err
				}
				if ok {
					plan.content = &StorageWrite{Type: WriteDocumentChange, ID: plan.content.ID, Content: &DocumentContent{Kind: ContentBase, Version: plan.change.version, Value: value}}
				}
			}
		}
		if plan.content != nil {
			writes = append(writes, *plan.content)
		}
		if plan.retire {
			writes = append(writes, StorageWrite{Type: WriteDocumentRetire, ID: planID(plan)})
		}
	}
	return writes, nil
}

func (tx *Transaction) planDocument(document *documentEntry) (*documentPlan, error) {
	target := document.target
	if target == nil {
		return nil, nil
	}
	plan := &documentPlan{addressID: document.addressID, retire: document.retireOnCommit}
	switch target.kind {
	case targetCreated:
		value, err := preparedObject(document.prepared)
		if err != nil {
			return nil, err
		}
		content := &StorageWrite{Type: WriteDocumentCreate, Record: &target.record, Content: &DocumentContent{Kind: ContentBase, Version: target.version, Value: value}}
		plan.create = &target.record
		plan.content = content
		plan.change = &planChange{tracker: target.tracker, prepared: document.prepared, version: target.version}
	case targetForkCopy:
		plan.create = &target.record
		source := target.source
		plan.content = &StorageWrite{Type: WriteDocumentCopy, Record: &target.record, Source: &source}
	case targetRetireOnly:
		record := target.retireRecord
		plan.record = &record
	case targetLoaded:
		loaded := target.loaded
		definition := document.definition
		prepared := document.prepared
		version := definition.Version
		id := loaded.record.ID
		record := loaded.record
		plan.record = &record
		plan.change = &planChange{tracker: loaded.tracker, prepared: prepared, version: version, loaded: loaded, definition: definition}
		// A version change stores a base even without operations; otherwise only
		// a change stores a delta.
		if loaded.storedVersion < version {
			value, err := preparedObject(prepared)
			if err != nil {
				return nil, err
			}
			plan.content = &StorageWrite{Type: WriteDocumentChange, ID: id, Content: &DocumentContent{Kind: ContentBase, Version: version, Value: value}}
		} else if ops := prepared.Ops(); len(ops) > 0 {
			plan.content = &StorageWrite{Type: WriteDocumentChange, ID: id, Content: &DocumentContent{Kind: ContentDelta, Version: version, Ops: ops}}
		}
	}
	return plan, nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// validateOwners requires live owned work to reference a non-terminal,
// non-completing, non-abort-marked owner, judged on the owner's final candidate.
func (tx *Transaction) validateOwners() error {
	type owner struct {
		what   string
		taskID TaskID
	}
	owners := []owner{}
	for index := range tx.writes {
		write := tx.writes[index]
		if write.Type != WriteConversation || write.Conversation == nil || write.Conversation.Owner == nil {
			continue
		}
		owners = append(owners, owner{what: "Conversation owner task", taskID: write.Conversation.Owner.TaskID})
	}
	for _, task := range tx.tasksByID {
		if task.write != nil && task.write.kind == taskWriteCreate && task.write.record.Owner != nil {
			owners = append(owners, owner{what: "Task owner", taskID: *task.write.record.Owner})
		}
	}
	for _, reference := range owners {
		task, err := tx.currentTask(tx.ctx, reference.taskID)
		if err != nil {
			return err
		}
		if task == nil {
			return fmt.Errorf("%s %d does not exist", reference.what, reference.taskID)
		}
		if task.State.Status == TaskStatusTerminal {
			return fmt.Errorf("%s %d is %s", reference.what, reference.taskID, task.State.Status)
		}
		if task.AbortRequested {
			return fmt.Errorf("%s %d is abort-marked", reference.what, reference.taskID)
		}
	}
	return nil
}

func (tx *Transaction) rejectForkSourceWrites(plans []*documentPlan) error {
	for _, plan := range plans {
		if plan.content == nil && !plan.retire {
			continue
		}
		id := planID(plan)
		if _, ok := tx.forkSourceDocumentIDs[id]; ok {
			return fmt.Errorf("Cannot change fork source document %d in the fork transaction", id)
		}
		scope := planScope(plan)
		if scope.Kind != ScopeConversation {
			continue
		}
		if _, ok := tx.forkSourceConversationIDs[scope.ConversationID]; ok && planFork(plan) == ForkCurrent {
			return fmt.Errorf("Cannot fork conversation %d while changing its current-policy documents", scope.ConversationID)
		}
	}
	return nil
}

func (tx *Transaction) abortChanges() {
	for _, document := range tx.documents {
		if document.change != nil {
			document.change.Abort()
		}
	}
}

func (tx *Transaction) assertOpen() error {
	if tx.sealed {
		return errors.New("Transaction has settled")
	}
	return nil
}

func (tx *Transaction) assertTaskDocumentsOpen(address DocumentAddress) error {
	if address.Scope.Kind != ScopeTask {
		return nil
	}
	if task := tx.tasksByID[address.Scope.TaskID]; task != nil && task.write != nil && task.write.record.State.Status == TaskStatusTerminal {
		return fmt.Errorf("Task %d is terminal", address.Scope.TaskID)
	}
	return nil
}

func (tx *Transaction) beginRead(method string) error {
	if err := tx.assertOpen(); err != nil {
		return err
	}
	if tx.hasTableWrite {
		return &ReadAfterWrite{Method: method}
	}
	return nil
}

func (tx *Transaction) beginWrite() error {
	if err := tx.assertOpen(); err != nil {
		return err
	}
	tx.hasTableWrite = true
	return nil
}

func (tx *Transaction) requireConversation(ctx context.Context, id ConversationID) error {
	if _, created := tx.createdConversationIDs[id]; created {
		return nil
	}
	record, err := tx.session.storage.Conversation(ctx, id)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("Conversation %d does not exist", id)
	}
	return nil
}

func (tx *Transaction) taskEntry(id TaskID) *transactionTask {
	task := tx.tasksByID[id]
	if task == nil {
		task = &transactionTask{}
		tx.tasksByID[id] = task
	}
	return task
}

func (tx *Transaction) currentTask(ctx context.Context, id TaskID) (*TaskRecord, error) {
	if task := tx.tasksByID[id]; task != nil && task.write != nil {
		return task.write.record, nil
	}
	return tx.committedTask(ctx, id)
}

func (tx *Transaction) committedTask(ctx context.Context, id TaskID) (*TaskRecord, error) {
	task := tx.taskEntry(id)
	if !task.committedLoad {
		record, err := tx.session.storage.Task(ctx, id)
		if err != nil {
			return nil, err
		}
		task.committedRead = record
		task.committedLoad = true
	}
	return task.committedRead, nil
}

// ─── Planning helpers ───────────────────────────────────────────────────────

func planID(plan *documentPlan) DocumentID {
	if plan.create != nil {
		return plan.create.ID
	}
	return plan.record.ID
}

func planScope(plan *documentPlan) DocumentScope {
	if plan.create != nil {
		return plan.create.Scope
	}
	return plan.record.Scope
}

func planFork(plan *documentPlan) string {
	if plan.create != nil {
		return plan.create.Fork
	}
	return plan.record.Fork
}

func publishes(plan *documentPlan) bool {
	return plan.retire || plan.change == nil || plan.change.loaded == nil || plan.content != nil
}

func preparedObject(prepared *delta.Prepared) (JsonObject, error) {
	if prepared == nil {
		return nil, errors.New("document change was not prepared")
	}
	value := prepared.Value()
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("document value is not a JSON object")
	}
	return JsonObject(object), nil
}

func applySubmissionChange(current *SubmissionRecord, change submissionChangeValue) (*SubmissionRecord, error) {
	if current.Status == SubmissionStatusDone || current.Status == SubmissionStatusUnanswered {
		return current, nil
	}
	if change.placed {
		if current.Status != SubmissionStatusQueued {
			return nil, fmt.Errorf("Submission %d is not queued", current.ID)
		}
		status := SubmissionStatusDone
		if current.Type == SubmissionTypeInput {
			status = SubmissionStatusPlaced
		}
		next := *current
		next.Status = status
		entry := change.entry
		next.Entry = &entry
		return &next, nil
	}
	settlement := change.settle
	if settlement.Status == SubmissionStatusDone && current.Status != SubmissionStatusPlaced {
		return nil, fmt.Errorf("Submission %d is not a placed input", current.ID)
	}
	next := *current
	next.Status = settlement.Status
	if settlement.Answer != nil {
		next.Answer = cloneID(settlement.Answer)
	}
	if settlement.Reason != "" {
		next.Reason = settlement.Reason
	}
	if settlement.Detail != nil {
		next.Detail = cloneRaw(settlement.Detail)
	}
	return &next, nil
}

func documentRecordFromCreate(create DocumentCreate, seq Seq) DocumentRecord {
	return DocumentRecord{
		ID:        create.ID,
		Kind:      create.Kind,
		Key:       cloneString(create.Key),
		Scope:     create.Scope,
		History:   create.History,
		Fork:      create.Fork,
		CreatedAt: seq,
	}
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	out := make(json.RawMessage, len(value))
	copy(out, value)
	return out
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneID(value *ID) *ID {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneSettlement(value SubmissionSettlement) SubmissionSettlement {
	out := value
	out.Answer = cloneID(value.Answer)
	out.Detail = cloneRaw(value.Detail)
	return out
}

func cloneTaskRecord(value TaskRecord) TaskRecord {
	out := value
	out.Input = cloneRaw(value.Input)
	out.Owner = cloneID(value.Owner)
	out.State.Checkpoint = cloneRaw(value.State.Checkpoint)
	if value.State.On != nil {
		out.State.On = append([]TaskID(nil), value.State.On...)
	}
	if value.State.Outcome != nil {
		outcome := *value.State.Outcome
		outcome.Result = cloneRaw(value.State.Outcome.Result)
		if value.State.Outcome.Error != nil {
			taskError := *value.State.Outcome.Error
			taskError.Detail = cloneRaw(value.State.Outcome.Error.Detail)
			outcome.Error = &taskError
		}
		out.State.Outcome = &outcome
	}
	if value.Memos != nil {
		memos := make(map[string]json.RawMessage, len(value.Memos))
		for name, memo := range value.Memos {
			memos[name] = cloneRaw(memo)
		}
		out.Memos = memos
	}
	return out
}

func stringPtrEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
