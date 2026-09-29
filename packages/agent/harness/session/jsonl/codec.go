// This file carries jsonl/codec.ts: JSONL header detection and parsing.
package jsonl

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// LegacyV3SessionHeader is the format-3 session header.
type LegacyV3SessionHeader struct {
	Type          string  `json:"type"`
	Version       int     `json:"version"`
	ID            string  `json:"id"`
	Timestamp     string  `json:"timestamp"`
	Cwd           string  `json:"cwd"`
	ParentSession *string `json:"parentSession,omitempty"`
}

// JsonlParsedSessionHeader is either a format-4 header or a legacy v3 header.
type JsonlParsedSessionHeader struct {
	Format string
	V4     *JsonlStorageHeader
	V3     *LegacyV3SessionHeader
}

func asRecord(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case JsonlStorageHeader:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, false
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, false
		}
		return out, true
	case *JsonlStorageHeader:
		if typed == nil {
			return nil, false
		}
		return asRecord(*typed)
	case LegacyV3SessionHeader:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, false
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, false
		}
		return out, true
	case *LegacyV3SessionHeader:
		if typed == nil {
			return nil, false
		}
		return asRecord(*typed)
	default:
		return nil, false
	}
}

func isSafeIntegerAtLeast(value any, minimum float64) bool {
	number, ok := value.(float64)
	if !ok {
		return false
	}
	if math.Trunc(number) != number || math.IsInf(number, 0) || math.IsNaN(number) {
		return false
	}
	if number < minimum {
		return false
	}
	return math.Abs(number) <= float64(1<<53)
}

func isParseableTimestamp(value string) bool {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123, time.ANSIC, time.UnixDate} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// IsLegacyV3SessionHeader reports whether value is a valid v3 header.
func IsLegacyV3SessionHeader(value any) bool {
	record, ok := asRecord(value)
	if !ok {
		return false
	}
	if record["type"] != "session" {
		return false
	}
	version, ok := record["version"].(float64)
	if !ok || version != 3 {
		return false
	}
	_, idOK := record["id"].(string)
	_, cwdOK := record["cwd"].(string)
	timestamp, timestampOK := record["timestamp"].(string)
	if !idOK || !cwdOK || !timestampOK || !isParseableTimestamp(timestamp) {
		return false
	}
	if parent, present := record["parentSession"]; present && parent != nil {
		if _, ok := parent.(string); !ok {
			return false
		}
	}
	return true
}

// IsJsonlStorageHeader reports whether value is a valid format-4 header.
func IsJsonlStorageHeader(value any) bool {
	record, ok := asRecord(value)
	if !ok {
		return false
	}
	if record["kind"] != "header" {
		return false
	}
	v, ok := record["v"].(float64)
	if !ok || v != float64(JSONL_FORMAT_VERSION) {
		return false
	}
	if _, ok := record["id"].(string); !ok {
		return false
	}
	if _, ok := record["cwd"].(string); !ok {
		return false
	}
	if !isSafeIntegerAtLeast(record["storageVersion"], 1) {
		return false
	}
	if !isSafeIntegerAtLeast(record["createdAt"], 0) {
		return false
	}
	if nextSeq, present := record["nextSeq"]; present && nextSeq != nil {
		if !isSafeIntegerAtLeast(nextSeq, 1) {
			return false
		}
	}
	if parent, present := record["parentSessionId"]; present && parent != nil {
		if _, ok := parent.(string); !ok {
			return false
		}
	}
	if legacy, present := record["legacyParentSessionPath"]; present && legacy != nil {
		if _, ok := legacy.(string); !ok {
			return false
		}
	}
	return true
}

// ParseJsonlSessionHeader parses one JSONL header line.
func ParseJsonlSessionHeader(line string) (JsonlParsedSessionHeader, error) {
	var value any
	if err := json.Unmarshal([]byte(line), &value); err != nil {
		return JsonlParsedSessionHeader{}, fmt.Errorf("Invalid JSONL session header: not valid JSON: %w", err)
	}
	if IsJsonlStorageHeader(value) {
		var header JsonlStorageHeader
		if err := json.Unmarshal([]byte(line), &header); err != nil {
			return JsonlParsedSessionHeader{}, err
		}
		return JsonlParsedSessionHeader{Format: "v4", V4: &header}, nil
	}
	if IsLegacyV3SessionHeader(value) {
		var header LegacyV3SessionHeader
		if err := json.Unmarshal([]byte(line), &header); err != nil {
			return JsonlParsedSessionHeader{}, err
		}
		return JsonlParsedSessionHeader{Format: "v3-legacy", V3: &header}, nil
	}
	return JsonlParsedSessionHeader{}, errors.New("Unsupported JSONL session header")
}

var _ = harnesstypes.FileError{}
