package main

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// walkAtoms calls fn for each MP4 atom (box) in data, passing the 4-byte
// type string and the atom body (bytes after the size+type header). Return
// false from fn to stop early.
func walkAtoms(data []byte, fn func(typ string, body []byte) bool) {
	pos := 0
	for pos+8 <= len(data) {
		size := binary.BigEndian.Uint32(data[pos : pos+4])
		typ := string(data[pos+4 : pos+8])
		headerLen := 8

		var end int
		switch size {
		case 0:
			end = len(data)
		case 1:
			// Extended 64-bit size follows the type field.
			if pos+16 > len(data) {
				return
			}
			extSize := binary.BigEndian.Uint64(data[pos+8 : pos+16])
			if extSize < 16 || uint64(pos)+extSize > uint64(len(data)) {
				return
			}
			headerLen = 16
			end = pos + int(extSize)
		default:
			if int(size) < 8 || pos+int(size) > len(data) {
				return
			}
			end = pos + int(size)
		}

		if !fn(typ, data[pos+headerLen:end]) {
			return
		}
		pos = end
	}
}

func findAtom(data []byte, typ string) []byte {
	var result []byte
	walkAtoms(data, func(t string, body []byte) bool {
		if t == typ {
			result = body
			return false
		}
		return true
	})
	return result
}

// parseM4ARating walks the moov→udta→meta→ilst hierarchy and resolves a star
// rating from freeform (----) atoms. Recognised atom names:
//
//   - "FMPS_Rating" (any case) → MediaMonkey, float 0.0–1.0
//   - "rating" (lowercase)     → iTunes, integer 0/20/40/60/80/100
//   - "RATING" (uppercase)     → foobar2000, integer 1–5
func parseM4ARating(data []byte, tagOrder []string) (int, bool) {
	moov := findAtom(data, "moov")
	if moov == nil {
		return 0, false
	}
	udta := findAtom(moov, "udta")
	if udta == nil {
		return 0, false
	}
	meta := findAtom(udta, "meta")
	// meta is a FullBox: the first 4 bytes are version+flags, not a child atom.
	if len(meta) < 4 {
		return 0, false
	}
	ilst := findAtom(meta[4:], "ilst")
	if ilst == nil {
		return 0, false
	}

	found := map[string]int{}

	walkAtoms(ilst, func(typ string, body []byte) bool {
		if typ != "----" {
			return true
		}
		var name string
		var valueData []byte
		walkAtoms(body, func(t string, b []byte) bool {
			switch t {
			case "name":
				// name is a FullBox: skip 4-byte version/flags.
				if len(b) >= 4 {
					name = strings.TrimSpace(string(b[4:]))
				}
			case "data":
				// data FullBox: 4-byte type indicator + 4-byte locale before value.
				if len(b) >= 8 {
					valueData = b[8:]
				}
			}
			return true
		})

		value := strings.TrimSpace(string(valueData))
		nameUpper := strings.ToUpper(name)
		switch {
		case nameUpper == "FMPS_RATING":
			if stars, ok := fmpsToStars(value); ok {
				found["MediaMonkey"] = stars
			}
		case name == "rating":
			// iTunes writes lowercase "rating" with 0/20/40/60/80/100 integer scale.
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err == nil && n > 0 {
				if stars := popmITunesToStars(byte(n)); stars > 0 {
					found["iTunes"] = stars
				}
			}
		case nameUpper == "RATING":
			// foobar2000 writes uppercase "RATING" with 1–5 integer scale.
			if stars, ok := ratingIntToStars(value); ok {
				found["foobar2000"] = stars
			}
		}
		return true
	})

	for _, format := range tagOrder {
		if stars, ok := found[format]; ok {
			return stars, true
		}
	}
	return 0, false
}
