package durable

import (
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/chord/delta"
)

// ValidateDocumentDefinition rejects a definition whose version is not a
// positive safe integer. Definitions are supplied explicitly and validated at
// construction, mirroring upstream `validateDefinition`.
func ValidateDocumentDefinition(def *DocumentDefinition) error {
	if def == nil {
		return fmt.Errorf("document definition is nil")
	}
	if def.Version < 1 || int64(def.Version) > MaxSafeInteger {
		return fmt.Errorf("Document %s version must be a positive integer", def.Kind)
	}
	return nil
}

// DefineDoc validates and returns a singleton definition. It is the Go
// counterpart of upstream `defineDoc`; the returned value is the token passed
// explicitly to typed access.
func DefineDoc(def DocumentDefinition) (*DocumentDefinition, error) {
	clone := def
	clone.Family = false
	if err := ValidateDocumentDefinition(&clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

// DefineDocFamily validates and returns a keyed family definition. It is the Go
// counterpart of upstream `defineDocFamily`; Initial receives the family seed.
func DefineDocFamily(def DocumentDefinition) (*DocumentDefinition, error) {
	clone := def
	clone.Family = true
	if err := ValidateDocumentDefinition(&clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

// SessionAddress builds the logical address of a session-scoped document.
func SessionAddress(def *DocumentDefinition) DocumentAddress {
	return DocumentAddress{Kind: def.Kind, Scope: DocumentScope{Kind: ScopeSession}}
}

// ConversationAddress builds the logical address of a conversation document or
// one keyed family member. A nil key selects the singleton.
func ConversationAddress(def *DocumentDefinition, conversationID ConversationID, key *string) DocumentAddress {
	return DocumentAddress{
		Kind:  def.Kind,
		Scope: DocumentScope{Kind: ScopeConversation, ConversationID: conversationID},
		Key:   key,
	}
}

// TaskAddress builds the logical address of a task document or one keyed family
// member. A nil key selects the singleton.
func TaskAddress(def *DocumentDefinition, taskID TaskID, key *string) DocumentAddress {
	return DocumentAddress{
		Kind:  def.Kind,
		Scope: DocumentScope{Kind: ScopeTask, TaskID: taskID},
		Key:   key,
	}
}

// ScopeKey returns the stable string identity of one document scope, matching
// the upstream `scopeKey` JSON encoding.
func ScopeKey(scope DocumentScope) string {
	switch scope.Kind {
	case ScopeConversation:
		return fmt.Sprintf("[\"conversation\",%d]", int64(scope.ConversationID))
	case ScopeTask:
		return fmt.Sprintf("[\"task\",%d]", int64(scope.TaskID))
	default:
		return "[\"session\"]"
	}
}

// AddressID returns the stable string identity of one logical address, matching
// the upstream `addressId` encoding. A nil Key is distinct from a present empty
// key.
func AddressID(address DocumentAddress) string {
	owner := "null"
	switch address.Scope.Kind {
	case ScopeConversation:
		owner = fmt.Sprintf("%d", int64(address.Scope.ConversationID))
	case ScopeTask:
		owner = fmt.Sprintf("%d", int64(address.Scope.TaskID))
	}
	key := "null"
	if address.Key != nil {
		encoded, err := json.Marshal(*address.Key)
		if err == nil {
			key = string(encoded)
		}
	}
	kind, _ := json.Marshal(address.Kind)
	return fmt.Sprintf("[%s,%s,%s,%s]", kind, scopeKindJSON(address.Scope.Kind), owner, key)
}

func scopeKindJSON(kind string) string {
	encoded, err := json.Marshal(kind)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// DocumentCreateFor builds the storage create record for a new incarnation at an
// address. Conversation documents carry the definition's history and fork
// policies; session and task documents do not.
func DocumentCreateFor(def *DocumentDefinition, address DocumentAddress, id DocumentID) DocumentCreate {
	record := DocumentCreate{ID: id, Kind: address.Kind, Scope: address.Scope, Key: address.Key}
	if address.Scope.Kind == ScopeConversation {
		record.History = def.History
		record.Fork = def.Fork
	}
	return record
}

// CheckSemantics rejects a stored record whose scope, history, or fork
// disagrees with the supplied definition.
func CheckSemantics(def *DocumentDefinition, id DocumentID, kind string, scope DocumentScope, history, fork string) error {
	if scope.Kind != def.Scope {
		return fmt.Errorf("Document %d (%s) does not match the supplied definition semantics", id, kind)
	}
	if scope.Kind == ScopeConversation && (history != def.History || fork != def.Fork) {
		return fmt.Errorf("Document %d (%s) does not match the supplied definition semantics", id, kind)
	}
	return nil
}

// CheckScope rejects a stored create record whose semantics disagree with the
// supplied definition.
func CheckScope(def *DocumentDefinition, create DocumentCreate) error {
	return CheckSemantics(def, create.ID, create.Kind, create.Scope, create.History, create.Fork)
}

// CheckRecordScope rejects a stored record whose semantics disagree with the
// supplied definition.
func CheckRecordScope(def *DocumentDefinition, record DocumentRecord) error {
	return CheckSemantics(def, record.ID, record.Kind, record.Scope, record.History, record.Fork)
}

// CheckVersion rejects a stored version the supplied definition cannot use.
func CheckVersion(def *DocumentDefinition, id DocumentID, kind string, version int) error {
	if version > def.Version {
		return fmt.Errorf("Document %d (%s) has newer version %d than %d", id, kind, version, def.Version)
	}
	if version < def.Version && def.Migrate == nil {
		return fmt.Errorf("Document %d (%s) requires migration from version %d", id, kind, version)
	}
	return nil
}

// MaterializeDocument validates and materializes a detached stored value for
// typed access, running migration when the stored version is older.
func MaterializeDocument(def *DocumentDefinition, kind string, scope DocumentScope, history, fork string, id DocumentID, version int, value JsonObject) (JsonObject, error) {
	if err := CheckSemantics(def, id, kind, scope, history, fork); err != nil {
		return nil, err
	}
	if err := CheckVersion(def, id, kind, version); err != nil {
		return nil, err
	}
	if version == def.Version {
		return value, nil
	}
	migrated, err := def.Migrate(value, version)
	if err != nil {
		return nil, err
	}
	copied, err := chord.CopyJSON(map[string]any(migrated))
	if err != nil {
		return nil, err
	}
	object, ok := copied.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Document %d (%s) migration did not return a JSON object", id, kind)
	}
	return JsonObject(object), nil
}

// DocumentDraft is one private mutable working copy of a document value backed
// by a single Chord change. Callers edit the draft; the owning Session prepares,
// detaches, and adopts it at its commit boundary.
//
// Go cannot revoke an escaped map reference the way a JavaScript Proxy can, so
// the Session's documented adaptation is to detach candidate data during
// preparation and reject Value after sealing.
type DocumentDraft struct {
	tracker *delta.Tracker
	change  *delta.Change
	sealed  bool
}

func newDocumentDraft(tracker *delta.Tracker, change *delta.Change) *DocumentDraft {
	return &DocumentDraft{tracker: tracker, change: change}
}

// Value returns the live private working copy of the draft. Mutating it edits
// the candidate revision until the owning Session prepares the change, at which
// point the candidate is detached and this handle is sealed.
func (d *DocumentDraft) Value() (JsonObject, error) {
	if d == nil || d.change == nil {
		return nil, fmt.Errorf("document draft is not backed by a change")
	}
	if d.sealed {
		return nil, fmt.Errorf("document draft is sealed")
	}
	value, err := d.change.Value()
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("document draft value is not a JSON object")
	}
	return JsonObject(object), nil
}
