package main

import (
	"bytes"
	"encoding/binary"
)

// parseMP4Rating looks for the iTunes rating atom (rtng) inside an M4A/MP4
// file and converts it to 1–5 Navidrome stars.
//
// iTunes stores a rating as a uint8 in the 'rtng' atom inside the 'udta/meta/
// ilst' hierarchy.  Values: 0 = none, 20 = 1 star, 40 = 2, 60 = 3, 80 = 4,
// 100 = 5 stars.
//
// As a fallback, FMPS_Rating stored in a '----' (freeform) atom is also
// recognised.
func parseMP4Rating(data []byte) (int, bool) {
	// Look for an 'rtng' atom anywhere in the file.
	if stars, ok := findRtngAtom(data); ok {
		return stars, true
	}
	// Fallback: freeform '----' atom with name "FMPS_Rating".
	if stars, ok := findFreeformFMPS(data); ok {
		return stars, true
	}
	return 0, false
}

// ─── rtng atom ────────────────────────────────────────────────────────────────

func findRtngAtom(data []byte) (int, bool) {
	rtng := []byte("rtng")
	idx := bytes.Index(data, rtng)
	for idx >= 0 {
		// The data atom is typically inside a 'data' child; its value byte is
		// 24 bytes after the 'rtng' name start (8-byte rtng box header +
		// 8-byte data box header + 4-byte type indicator + 4-byte locale = 24,
		// but layouts vary).  We look ahead for the 'data' atom inside 'rtng'.
		atomStart := idx - 4 // 4-byte size precedes the name
		if atomStart < 0 {
			idx = bytes.Index(data[idx+4:], rtng)
			if idx >= 0 {
				idx += (atomStart + 4) + 4
			}
			continue
		}
		rtngSize := int(binary.BigEndian.Uint32(data[atomStart : atomStart+4]))
		if rtngSize < 8 || atomStart+rtngSize > len(data) {
			break
		}
		rtngBody := data[atomStart+8 : atomStart+rtngSize]

		// Find 'data' child atom.
		dataIdx := bytes.Index(rtngBody, []byte("data"))
		if dataIdx < 4 {
			break
		}
		dataStart := dataIdx - 4
		dataSize := int(binary.BigEndian.Uint32(rtngBody[dataStart : dataStart+4]))
		// data atom: 4-byte size + 4-byte "data" + 4-byte type + 4-byte locale = 16 bytes header
		if dataSize < 17 || dataStart+dataSize > len(rtngBody) {
			break
		}
		ratingByte := rtngBody[dataStart+16]
		return itunesRatingToStars(ratingByte), ratingByte > 0
	}
	return 0, false
}

// itunesRatingToStars converts an iTunes rtng byte (0/20/40/60/80/100) to
// 1–5 stars.
func itunesRatingToStars(b byte) int {
	switch {
	case b == 0:
		return 0
	case b <= 20:
		return 1
	case b <= 40:
		return 2
	case b <= 60:
		return 3
	case b <= 80:
		return 4
	default:
		return 5
	}
}

// ─── Freeform '----' atom ─────────────────────────────────────────────────────

// findFreeformFMPS looks for a freeform atom ('----') whose 'name' atom
// contains "FMPS_Rating" and whose 'data' atom contains a float string.
func findFreeformFMPS(data []byte) (int, bool) {
	fourDash := []byte("----")
	pos := 0
	for pos+8 <= len(data) {
		idx := bytes.Index(data[pos:], fourDash)
		if idx < 0 {
			break
		}
		atomName := data[pos+idx:]
		atomStart := pos + idx - 4
		if atomStart < 0 {
			pos += idx + 4
			continue
		}
		atomSize := int(binary.BigEndian.Uint32(data[atomStart : atomStart+4]))
		if atomSize < 8 || atomStart+atomSize > len(data) {
			pos += idx + 4
			continue
		}
		body := data[atomStart+8 : atomStart+atomSize]

		// Child: 'mean' (domain), 'name' (tag name), 'data' (value).
		if containsFreeformName(body, "FMPS_Rating") {
			if val, ok := extractFreeformData(body); ok {
				return fmpsToStars(val)
			}
		}
		pos = atomStart + atomSize
	}
	return 0, false
}

func containsFreeformName(body []byte, name string) bool {
	nameAtom := []byte("name")
	idx := bytes.Index(body, nameAtom)
	for idx >= 4 {
		start := idx - 4
		sz := int(binary.BigEndian.Uint32(body[start : start+4]))
		if sz < 12 || start+sz > len(body) {
			return false
		}
		// 'name' atom: 4-byte size + 4-byte "name" + 4-byte flags + value
		nameVal := string(body[start+12 : start+sz])
		if nameVal == name {
			return true
		}
		idx = bytes.Index(body[idx+4:], nameAtom)
		if idx >= 0 {
			idx += (start + 4) + 4
		}
	}
	return false
}

func extractFreeformData(body []byte) (string, bool) {
	dataAtom := []byte("data")
	idx := bytes.Index(body, dataAtom)
	if idx < 4 {
		return "", false
	}
	start := idx - 4
	sz := int(binary.BigEndian.Uint32(body[start : start+4]))
	// data atom: 4-byte size + 4-byte "data" + 4-byte type + 4-byte locale = 16-byte header
	if sz < 17 || start+sz > len(body) {
		return "", false
	}
	return string(body[start+16 : start+sz]), true
}
