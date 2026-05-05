package main

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// parseID3v2Rating inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating if it finds either:
//
//   - a TXXX frame whose description is "FMPS_Rating" (float 0.0–1.0), or
//   - a POPM (Popularimeter) frame.
//
// TXXX FMPS_Rating takes precedence over POPM when both are present.
func parseID3v2Rating(data []byte) (int, bool) {
	if len(data) < 10 {
		return 0, false
	}
	// ID3v2 magic
	if !bytes.HasPrefix(data, []byte("ID3")) {
		return 0, false
	}

	majorVer := data[3] // 2, 3, or 4
	// data[4] = minor version (ignored)
	// data[5] = flags (bit 6 = extended header; we skip it)
	flags := data[5]

	// Syncsafe integer: each byte only uses 7 bits.
	tagSize := syncsafeInt(data[6:10])

	payload := data[10:]
	if len(payload) > tagSize {
		payload = payload[:tagSize]
	}

	// Skip extended header (ID3v2.3+, flag bit 6).
	if majorVer >= 3 && flags&0x40 != 0 && len(payload) >= 4 {
		extSize := int(binary.BigEndian.Uint32(payload[:4]))
		if extSize+4 <= len(payload) {
			payload = payload[4+extSize:]
		}
	}

	var fmpsStars, popmStars int
	var fmpsOK, popmOK bool

	for len(payload) >= 10 {
		// Frame header: 4-byte ID + 4-byte size + 2-byte flags.
		frameID := string(payload[:4])
		if frameID == "\x00\x00\x00\x00" {
			break // padding
		}

		var frameSize int
		if majorVer >= 4 {
			frameSize = syncsafeInt(payload[4:8])
		} else {
			frameSize = int(binary.BigEndian.Uint32(payload[4:8]))
		}
		// frameFlags := payload[8:10]  // unused for our purposes

		if frameSize <= 0 || 10+frameSize > len(payload) {
			break
		}

		frameData := payload[10 : 10+frameSize]

		switch frameID {
		case "TXXX":
			if d, ok := parseTXXXFMPS(frameData); ok {
				fmpsStars, fmpsOK = d, true
			}
		case "POPM":
			if s, ok := parsePOPM(frameData); ok && !popmOK {
				popmStars, popmOK = s, true
			}
		}

		payload = payload[10+frameSize:]
	}

	if fmpsOK {
		return fmpsStars, true
	}
	if popmOK {
		return popmStars, true
	}
	return 0, false
}

// parseTXXXFMPS extracts the FMPS_Rating value from a TXXX frame body.
// Frame body layout: <encoding byte> <description NUL> <value>
func parseTXXXFMPS(data []byte) (int, bool) {
	if len(data) < 2 {
		return 0, false
	}
	encoding := data[0]
	rest := data[1:]

	var desc, value string
	if encoding == 1 || encoding == 2 {
		// UTF-16: null terminator is two bytes.
		desc, value = splitUTF16NullSep(rest)
	} else {
		// Latin-1 or UTF-8: null terminator is one byte.
		idx := bytes.IndexByte(rest, 0x00)
		if idx < 0 {
			return 0, false
		}
		desc = string(rest[:idx])
		value = string(rest[idx+1:])
	}

	if !strings.EqualFold(strings.TrimSpace(desc), "FMPS_Rating") {
		return 0, false
	}
	return fmpsToStars(value)
}

// parsePOPM extracts the rating byte from a POPM (Popularimeter) frame.
// Frame body layout: <email NUL> <rating byte> [<play count bytes>]
//
// The email field identifies the application that wrote the frame and
// determines which byte→star scale to apply.
func parsePOPM(data []byte) (int, bool) {
	idx := bytes.IndexByte(data, 0x00)
	if idx < 0 || idx+1 >= len(data) {
		return 0, false
	}
	email := string(data[:idx])
	ratingByte := data[idx+1]
	return popmToStars(email, ratingByte)
}

// ─── UTF-16 helpers ───────────────────────────────────────────────────────────

// splitUTF16NullSep splits a byte slice at the first UTF-16 null (0x00 0x00)
// and decodes both halves as ASCII (sufficient for "FMPS_Rating" description
// and a decimal float value).
func splitUTF16NullSep(data []byte) (desc, value string) {
	// Scan for 0x00 0x00 on even boundary.
	start := 0
	// Skip BOM if present.
	if len(data) >= 2 && ((data[0] == 0xFF && data[1] == 0xFE) || (data[0] == 0xFE && data[1] == 0xFF)) {
		start = 2
	}
	for i := start; i+1 < len(data); i += 2 {
		if data[i] == 0 && data[i+1] == 0 {
			desc = utf16LEToASCII(data[start:i])
			rest := data[i+2:]
			// Skip BOM of value string if present.
			if len(rest) >= 2 && ((rest[0] == 0xFF && rest[1] == 0xFE) || (rest[0] == 0xFE && rest[1] == 0xFF)) {
				rest = rest[2:]
			}
			value = utf16LEToASCII(rest)
			return
		}
	}
	return "", ""
}

// utf16LEToASCII converts a UTF-16 LE byte sequence to a plain ASCII string by
// taking every first byte of each codepoint (valid only for ASCII range).
func utf16LEToASCII(data []byte) string {
	out := make([]byte, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		out = append(out, data[i])
	}
	return string(out)
}

// ─── Syncsafe integer ────────────────────────────────────────────────────────

// syncsafeInt decodes a 4-byte ID3 syncsafe integer (7 bits per byte).
func syncsafeInt(b []byte) int {
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3])
}
