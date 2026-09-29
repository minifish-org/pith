// This file is a Go port of packages/ai/src/utils/uuid.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"
)

const maxUUIDv7Timestamp = 0xffffffffffff
const maxSequence = (uint64(1) << 41) - 1

var (
	uuidMu                sync.Mutex
	lastOrdinaryTimestamp int64 = -1
	sequence              uint64
	sequenceInitialized   bool
)

// UUIDv7 generates a time-ordered UUIDv7. A supplied timestamp is preserved for
// follower ids. When timestampMs is nil the current clock is used, monotonic
// within the process.
func UUIDv7(timestampMs *float64) (string, error) {
	var requested int64
	if timestampMs == nil {
		requested = time.Now().UnixMilli()
	} else {
		value := *timestampMs
		if value != float64(int64(value)) || value < 0 || value > maxUUIDv7Timestamp {
			return "", fmt.Errorf("UUIDv7 timestamp must be an integer between 0 and %d", maxUUIDv7Timestamp)
		}
		requested = int64(value)
	}

	uuidMu.Lock()
	defer uuidMu.Unlock()

	effectiveTimestamp := requested
	if timestampMs == nil {
		if requested < lastOrdinaryTimestamp {
			effectiveTimestamp = lastOrdinaryTimestamp
		}
		lastOrdinaryTimestamp = effectiveTimestamp
	}

	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	if !sequenceInitialized {
		sequence = uint64(bytes[1])<<32 | uint64(bytes[2])<<24 | uint64(bytes[3])<<16 | uint64(bytes[4])<<8 | uint64(bytes[5])
		sequenceInitialized = true
	} else {
		if sequence == maxSequence {
			return "", errors.New("UUIDv7 generator sequence exhausted")
		}
		sequence++
	}

	timestamp := uint64(effectiveTimestamp)
	for index := 5; index >= 0; index-- {
		bytes[index] = byte(timestamp >> uint((5-index)*8))
	}
	bytes[6] = 0x70 | byte((sequence>>37)&0x0f)
	bytes[7] = byte((sequence >> 29) & 0xff)
	bytes[8] = 0x80 | byte((sequence>>23)&0x3f)
	bytes[9] = byte((sequence >> 15) & 0xff)
	bytes[10] = byte((sequence >> 7) & 0xff)
	bytes[11] = byte((sequence&0x7f)<<1) | (bytes[11] & 0x01)

	hexDigits := make([]string, 16)
	for i, b := range bytes {
		hexDigits[i] = fmt.Sprintf("%02x", b)
	}
	return hexDigits[0] + hexDigits[1] + hexDigits[2] + hexDigits[3] + "-" +
		hexDigits[4] + hexDigits[5] + "-" +
		hexDigits[6] + hexDigits[7] + "-" +
		hexDigits[8] + hexDigits[9] + "-" +
		hexDigits[10] + hexDigits[11] + hexDigits[12] + hexDigits[13] + hexDigits[14] + hexDigits[15], nil
}
