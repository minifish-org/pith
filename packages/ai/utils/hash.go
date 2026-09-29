// This file is a Go port of packages/ai/src/utils/hash.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import "strconv"

// ShortHash is a fast deterministic hash to shorten long strings. It reproduces
// the upstream cyrb53-style mixing with 32-bit multiplies and base-36 encoding
// of the two 32-bit halves.
func ShortHash(input string) string {
	var h1 uint32 = 0xdeadbeef
	var h2 uint32 = 0x41c6ce57
	for _, ch := range utf16Units(input) {
		h1 = imul32(h1^uint32(ch), 2654435761)
		h2 = imul32(h2^uint32(ch), 1597334677)
	}
	h1 = imul32(h1^(h1>>16), 2246822507) ^ imul32(h2^(h2>>13), 3266489909)
	h2 = imul32(h2^(h2>>16), 2246822507) ^ imul32(h1^(h1>>13), 3266489909)
	return strconv.FormatUint(uint64(h2), 36) + strconv.FormatUint(uint64(h1), 36)
}

// imul32 reproduces JavaScript's Math.imul: a 32-bit signed multiply whose low
// 32 bits are reinterpreted as unsigned for the bitwise operations above.
func imul32(a, b uint32) uint32 {
	return uint32(int32(a) * int32(b))
}

// utf16Units returns the UTF-16 code units of a string, matching JS
// `str.charCodeAt(i)` iteration. Each rune outside the BMP contributes a
// surrogate pair, exactly as the upstream loop sees them.
func utf16Units(input string) []uint16 {
	units := make([]uint16, 0, len(input))
	for _, r := range input {
		if r <= 0xffff {
			units = append(units, uint16(r))
			continue
		}
		r -= 0x10000
		units = append(units, uint16(0xd800+(r>>10)))
		units = append(units, uint16(0xdc00+(r&0x3ff)))
	}
	return units
}
