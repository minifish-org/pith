// Image signature sniffing for the Durable read tool.
//
// This is a Go port of packages/durable/src/tools/image.ts at Pi revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The read tool does not synthesize image content in this release; this helper
// only classifies the byte stream so the tool can report an unsupported-image
// diagnostic instead of decoding binary data as text.
package tools

import "encoding/base64"

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// DetectSupportedImageMimeType sniffs a supported image container and returns
// its MIME type, or nil when the bytes are not a supported image. Animated PNG
// and JPEG files carrying the 0xF7 extension marker are rejected because the
// providers cannot consume them.
func DetectSupportedImageMimeType(buffer []byte) *string {
	if startsWithBytes(buffer, []byte{0xff, 0xd8, 0xff}) {
		if len(buffer) > 3 && buffer[3] == 0xf7 {
			return nil
		}
		return mimePointer("image/jpeg")
	}
	if startsWithBytes(buffer, pngSignature) {
		if isPng(buffer) && !isAnimatedPng(buffer) {
			return mimePointer("image/png")
		}
		return nil
	}
	if startsWithAscii(buffer, 0, "GIF87a") || startsWithAscii(buffer, 0, "GIF89a") {
		return mimePointer("image/gif")
	}
	if startsWithAscii(buffer, 0, "RIFF") && startsWithAscii(buffer, 8, "WEBP") {
		return mimePointer("image/webp")
	}
	if startsWithAscii(buffer, 0, "BM") && isBmp(buffer) {
		return mimePointer("image/bmp")
	}
	return nil
}

// EncodeBase64 encodes bytes with the standard base64 alphabet.
func EncodeBase64(bytes []byte) string {
	return base64.StdEncoding.EncodeToString(bytes)
}

func mimePointer(value string) *string { return &value }

func isPng(buffer []byte) bool {
	return len(buffer) >= 16 && readUint32BE(buffer, len(pngSignature)) == 13 && startsWithAscii(buffer, 12, "IHDR")
}

func isAnimatedPng(buffer []byte) bool {
	offset := len(pngSignature)
	for offset+8 <= len(buffer) {
		chunkLength := int(readUint32BE(buffer, offset))
		chunkTypeOffset := offset + 4
		if startsWithAscii(buffer, chunkTypeOffset, "acTL") {
			return true
		}
		if startsWithAscii(buffer, chunkTypeOffset, "IDAT") {
			return false
		}
		nextOffset := offset + 8 + chunkLength + 4
		if nextOffset <= offset || nextOffset > len(buffer) {
			return false
		}
		offset = nextOffset
	}
	return false
}

func isBmp(buffer []byte) bool {
	if len(buffer) < 26 {
		return false
	}
	declaredFileSize := readUint32LE(buffer, 2)
	pixelDataOffset := readUint32LE(buffer, 10)
	dibHeaderSize := readUint32LE(buffer, 14)
	if declaredFileSize != 0 && declaredFileSize < 26 {
		return false
	}
	if pixelDataOffset < 14+dibHeaderSize {
		return false
	}
	if declaredFileSize != 0 && pixelDataOffset >= declaredFileSize {
		return false
	}

	var colorPlanes int
	var bitsPerPixel int
	if dibHeaderSize == 12 {
		colorPlanes = readUint16LE(buffer, 22)
		bitsPerPixel = readUint16LE(buffer, 24)
	} else if dibHeaderSize >= 40 && dibHeaderSize <= 124 {
		if len(buffer) < 30 {
			return false
		}
		colorPlanes = readUint16LE(buffer, 26)
		bitsPerPixel = readUint16LE(buffer, 28)
	} else {
		return false
	}
	if colorPlanes != 1 {
		return false
	}
	switch bitsPerPixel {
	case 1, 4, 8, 16, 24, 32:
		return true
	default:
		return false
	}
}

func readUint16LE(buffer []byte, offset int) int {
	return at(buffer, offset) + at(buffer, offset+1)<<8
}

func readUint32BE(buffer []byte, offset int) uint32 {
	return uint32(at(buffer, offset))<<24 | uint32(at(buffer, offset+1))<<16 | uint32(at(buffer, offset+2))<<8 | uint32(at(buffer, offset+3))
}

func readUint32LE(buffer []byte, offset int) int {
	return at(buffer, offset) + at(buffer, offset+1)<<8 + at(buffer, offset+2)<<16 + at(buffer, offset+3)<<24
}

func at(buffer []byte, offset int) int {
	if offset < 0 || offset >= len(buffer) {
		return 0
	}
	return int(buffer[offset])
}

func startsWithBytes(buffer []byte, prefix []byte) bool {
	if len(buffer) < len(prefix) {
		return false
	}
	for index, value := range prefix {
		if buffer[index] != value {
			return false
		}
	}
	return true
}

func startsWithAscii(buffer []byte, offset int, text string) bool {
	if len(buffer) < offset+len(text) {
		return false
	}
	for index := 0; index < len(text); index++ {
		if buffer[offset+index] != text[index] {
			return false
		}
	}
	return true
}
