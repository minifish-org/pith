package memory

import (
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/durable"
)

// lowerBound returns the first index whose value is >= target.
func lowerBound[T ~int64](ids []T, target T) int {
	low, high := 0, len(ids)
	for low < high {
		middle := int(uint(low+high) >> 1)
		if ids[middle] < target {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

// upperBound returns the first index whose value is > target.
func upperBound[T ~int64](ids []T, target T) int {
	low, high := 0, len(ids)
	for low < high {
		middle := int(uint(low+high) >> 1)
		if ids[middle] <= target {
			low = middle + 1
		} else {
			high = middle
		}
	}
	return low
}

// insertSorted inserts id into an ascending slice, preserving order.
func insertSorted[T ~int64](ids []T, id T) []T {
	if len(ids) == 0 || ids[len(ids)-1] < id {
		return append(ids, id)
	}
	index := lowerBound(ids, id)
	ids = append(ids, 0)
	copy(ids[index+1:], ids[index:])
	ids[index] = id
	return ids
}

// removeSorted removes id from an ascending slice when present.
func removeSorted[T ~int64](ids []T, id T) []T {
	index := lowerBound(ids, id)
	if index < len(ids) && ids[index] == id {
		copy(ids[index:], ids[index+1:])
		ids = ids[:len(ids)-1]
	}
	return ids
}

// insertMapID inserts id into the ascending slice stored under key.
func insertMapID[K comparable, T ~int64](index map[K][]T, key K, id T) {
	index[key] = insertSorted(index[key], id)
}

// removeMapID removes id from the ascending slice stored under key, dropping an
// empty entry.
func removeMapID[K comparable, T ~int64](index map[K][]T, key K, id T) {
	ids := removeSorted(index[key], id)
	if len(ids) == 0 {
		delete(index, key)
		return
	}
	index[key] = ids
}

// cursorID decodes the backend-owned continuation state of a scan. A nil or
// empty cursor means "from the start".
func cursorID(cursor durable.Cursor) (*durable.ID, error) {
	if len(cursor) == 0 {
		return nil, nil
	}
	var decoded struct {
		After *json.Number `json:"after"`
	}
	if err := json.Unmarshal(cursor, &decoded); err != nil {
		return nil, fmt.Errorf("Invalid storage cursor: %w", err)
	}
	if decoded.After == nil {
		return nil, nil
	}
	value, err := decoded.After.Int64()
	if err != nil || value < 0 || value > durable.MaxSafeInteger {
		return nil, fmt.Errorf("Invalid storage cursor")
	}
	id := durable.ID(value)
	return &id, nil
}

// cursorAfter builds the continuation state for a page.
func cursorAfter(id durable.ID) durable.Cursor {
	return durable.Cursor(fmt.Sprintf(`{"after":%d}`, int64(id)))
}

// pageItems slices a scanned value list into a detached page.
func pageItems[T any](values []T, limit int, idOf func(T) durable.ID) durable.Page[T] {
	if limit < 0 {
		limit = 0
	}
	if len(values) <= limit {
		return durable.Page[T]{Items: values}
	}
	items := values[:limit]
	var next durable.Cursor
	if len(items) > 0 {
		next = cursorAfter(idOf(items[len(items)-1]))
	}
	return durable.Page[T]{Items: items, Next: next}
}
