// This file is a Go port of packages/ai/src/utils/sanitize-unicode.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream operates on a JavaScript UTF-16 string; a lone surrogate is a
// first-class value there. A Go string is UTF-8, and a lone surrogate only
// appears as its CESU-8/WTF-8 encoding (three bytes `ED A0..BF 80..BF`), for
// example after a `\uD83D` escape is decoded by a lenient JSON reader. This port
// therefore scans the raw bytes: it removes surrogate encodings that are not a
// valid high+low pair, and rewrites a valid pair to its astral UTF-8 form.
package utils

import (
	"strings"
	"unicode/utf8"
)

// decodeSurrogateAt decodes a 3-byte CESU-8 surrogate at data[i:]. It returns
// the surrogate code unit and the byte length when the bytes form one.
func decodeSurrogateAt(data []byte, i int) (uint16, int, bool) {
	if i+3 > len(data) {
		return 0, 0, false
	}
	b0, b1, b2 := data[i], data[i+1], data[i+2]
	if b0 != 0xED {
		return 0, 0, false
	}
	if b1 < 0xA0 || b1 > 0xBF {
		return 0, 0, false
	}
	if b2 < 0x80 || b2 > 0xBF {
		return 0, 0, false
	}
	unit := uint16(0xD000) | uint16(b1&0x3F)<<6 | uint16(b2&0x3F)
	return unit, 3, true
}

// isHighSurrogate reports whether unit is in the high-surrogate range.
func isHighSurrogate(unit uint16) bool { return unit >= 0xD800 && unit <= 0xDBFF }

// isLowSurrogate reports whether unit is in the low-surrogate range.
func isLowSurrogate(unit uint16) bool { return unit >= 0xDC00 && unit <= 0xDFFF }

// SanitizeSurrogates removes unpaired Unicode surrogate characters from a
// string. Unpaired surrogates cause JSON serialization errors in many API
// providers; properly paired surrogates (emoji and other astral characters) are
// preserved.
func SanitizeSurrogates(text string) string {
	data := []byte(text)
	var builder strings.Builder
	builder.Grow(len(text))

	for i := 0; i < len(data); {
		if unit, size, ok := decodeSurrogateAt(data, i); ok {
			if isHighSurrogate(unit) {
				if nextUnit, nextSize, nextOK := decodeSurrogateAt(data, i+size); nextOK && isLowSurrogate(nextUnit) {
					builder.WriteRune(utf16Decode(unit, nextUnit))
					i += size + nextSize
					continue
				}
			}
			// Unpaired surrogate: drop it.
			i += size
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			// Preserve an undecodable byte as-is rather than dropping content;
			// upstream's regex only targets surrogates.
			builder.WriteByte(data[i])
			i++
			continue
		}
		builder.Write(data[i : i+size])
		i += size
	}
	return builder.String()
}

// utf16Decode combines a surrogate pair into a rune.
func utf16Decode(high, low uint16) rune {
	return rune(0x10000 + (uint32(high)-0xD800)<<10 + (uint32(low) - 0xDC00))
}
