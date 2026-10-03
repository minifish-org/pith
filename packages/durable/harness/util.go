package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
)

// Built-in entry kinds.
const (
	entryKindUser       = "pi.user"
	entryKindAssistant  = "pi.assistant"
	entryKindToolResult = "pi.tool-result"
	entryKindSystem     = "pi.system"
	entryKindReset      = "pi.reset"
	entryKindCompaction = "pi.compaction"
)

// userMessageFromContent reconstructs a user message from a stored content
// value (string or content-block array).
func userMessageFromContent(content any, now int64) types.Message {
	var user types.UserMessage
	if text, ok := content.(string); ok {
		user = types.NewUserMessage(text, float64(now))
	} else {
		blocks := decodeContentBlocks(content)
		user = types.NewUserMessageBlocks(blocks, float64(now))
	}
	return types.NewUserMessageVariant(user)
}

func decodeContentBlocks(content any) []types.ContentBlock {
	data, err := json.Marshal(content)
	if err != nil {
		return nil
	}
	var blocks []types.ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil
	}
	return blocks
}

func fmtErrorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// thinkingLevel converts a stored string into an AI thinking level.
func thinkingLevel(value string) types.ThinkingLevel { return types.ThinkingLevel(value) }

// str returns the string form of a JSON scalar, or "".
func str(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func asInt64(value any) (int64, bool) {
	if value == nil {
		return 0, false
	}
	f, ok := toNumber(value)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func asTaskID(value any) (durable.TaskID, bool) {
	n, ok := asInt64(value)
	return durable.TaskID(n), ok
}

func asSubmissionID(value any) (durable.SubmissionID, bool) {
	n, ok := asInt64(value)
	return durable.SubmissionID(n), ok
}

func asEntryID(value any) (durable.EntryID, bool) {
	n, ok := asInt64(value)
	return durable.EntryID(n), ok
}

func asConversationID(value any) (durable.ConversationID, bool) {
	n, ok := asInt64(value)
	return durable.ConversationID(n), ok
}

// idEqual compares a stored JSON id against a typed ID.
func idEqual(stored any, want any) bool {
	n, ok := asInt64(stored)
	if !ok {
		return false
	}
	m, ok := asInt64(want)
	return ok && n == m
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return str(value)
}

// errClosed is the shared closed error.
func closedError() error { return errors.New("Harness is closed") }

// scanAll collects every item of a paginated scan in page order.
func scanAll[T any](scan func(durable.Cursor) (durable.Page[T], error)) ([]T, error) {
	var items []T
	var cursor durable.Cursor
	for {
		page, err := scan(cursor)
		if err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if page.Next == nil {
			return items, nil
		}
		cursor = page.Next
	}
}

// waiters holds pending waits by key. Each settles once.
type waiters[K comparable, T any] struct {
	mu   sync.Mutex
	sets map[K][]chan T
}

func newWaiters[K comparable, T any]() *waiters[K, T] {
	return &waiters[K, T]{sets: map[K][]chan T{}}
}

// add registers a waiter; it returns the channel and a cancel function.
func (w *waiters[K, T]) add(key K) (<-chan T, func()) {
	ch := make(chan T, 1)
	w.mu.Lock()
	w.sets[key] = append(w.sets[key], ch)
	w.mu.Unlock()
	cancel := func() {
		w.mu.Lock()
		list := w.sets[key]
		for i, c := range list {
			if c == ch {
				w.sets[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(w.sets[key]) == 0 {
			delete(w.sets, key)
		}
		w.mu.Unlock()
	}
	return ch, cancel
}

func (w *waiters[K, T]) keys() []K {
	w.mu.Lock()
	defer w.mu.Unlock()
	keys := make([]K, 0, len(w.sets))
	for k := range w.sets {
		keys = append(keys, k)
	}
	return keys
}

func (w *waiters[K, T]) resolve(key K, value T) {
	w.mu.Lock()
	list := w.sets[key]
	delete(w.sets, key)
	w.mu.Unlock()
	for _, ch := range list {
		select {
		case ch <- value:
		default:
		}
		close(ch)
	}
}

func (w *waiters[K, T]) resolveAll(value T) {
	w.mu.Lock()
	sets := w.sets
	w.sets = map[K][]chan T{}
	w.mu.Unlock()
	for _, list := range sets {
		for _, ch := range list {
			select {
			case ch <- value:
			default:
			}
			close(ch)
		}
	}
}

// jsonEqual is structural equality of two JSON values; object key order is
// ignored.
func jsonEqual(left, right any) bool {
	return jsonDeepEqual(left, right)
}

func jsonDeepEqual(left, right any) bool {
	if left == nil && right == nil {
		return true
	}
	switch l := left.(type) {
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for k, lv := range l {
			rv, ok := r[k]
			if !ok || !jsonDeepEqual(lv, rv) {
				return false
			}
		}
		return true
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !jsonDeepEqual(l[i], r[i]) {
				return false
			}
		}
		return true
	}
	return numericOrValueEqual(left, right)
}

func numericOrValueEqual(left, right any) bool {
	lf, lok := toNumber(left)
	rf, rok := toNumber(right)
	if lok && rok {
		return lf == rf
	}
	return left == right
}

func toNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case durable.ID:
		return float64(v), true
	case durable.Seq:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

// copyRawJSON returns one detached JSON value as a map, or nil for empty input.
func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, nil
}

func mustJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("harness: marshal: %v", err))
	}
	return data
}

// errorText renders an error or arbitrary value as text.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

var _ = context.Background
