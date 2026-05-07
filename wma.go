package main

import (
	"bytes"
	"encoding/binary"
	"strings"
)

var (
	asfHeaderObjectGUID = []byte{
		0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11,
		0xA6, 0xD9, 0x00, 0xAA, 0x00, 0x62, 0xCE, 0x6C,
	}
	asfExtContentDescObjectGUID = []byte{
		0xD2, 0xD0, 0xA4, 0x40, 0xE3, 0x07, 0x11, 0xD2,
		0x97, 0xF0, 0x00, 0xA0, 0xC9, 0x5E, 0xA8, 0x50,
	}
)

// decodeUTF16LE converts a little-endian UTF-16 byte slice to a Go string,
// stopping at the first null character. Odd-length input is truncated.
func decodeUTF16LE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	runes := make([]rune, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		r := rune(binary.LittleEndian.Uint16(b[i : i+2]))
		if r == 0 {
			break
		}
		runes = append(runes, r)
	}
	return string(runes)
}

// parseWMARating walks the ASF Header Object looking for an Extended Content
// Description Object and extracts a star rating from it.
func parseWMARating(data []byte, tagOrder []string) (int, bool) {
	// ASF Header Object layout: 16-byte GUID + 8-byte size + 4-byte num child
	// objects + 2 reserved bytes = 30 bytes fixed.
	if len(data) < 30 || !bytes.Equal(data[:16], asfHeaderObjectGUID) {
		return 0, false
	}
	numHeaders := binary.LittleEndian.Uint32(data[24:28])
	pos := 30

	for i := uint32(0); i < numHeaders && pos+24 <= len(data); i++ {
		guid := data[pos : pos+16]
		objSize := int(binary.LittleEndian.Uint64(data[pos+16 : pos+24]))
		if objSize < 24 || pos+objSize > len(data) {
			break
		}
		if bytes.Equal(guid, asfExtContentDescObjectGUID) {
			return parseASFExtContentDesc(data[pos+24:pos+objSize], tagOrder)
		}
		pos += objSize
	}
	return 0, false
}

// parseASFExtContentDesc reads the body of an ASF Extended Content Description
// Object (after its 24-byte GUID+size header) and extracts a star rating.
// Recognised descriptors:
//
//   - "WM/SharedUserRating" (WORD/type 5): WMP 0/25/50/75/99 byte scale
//   - "FMPS_Rating"         (Unicode/type 0): MediaMonkey float 0.0–1.0
func parseASFExtContentDesc(body []byte, tagOrder []string) (int, bool) {
	if len(body) < 2 {
		return 0, false
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	pos := 2
	found := map[string]int{}

	for i := 0; i < count; i++ {
		if pos+4 > len(body) {
			break
		}
		nameLen := int(binary.LittleEndian.Uint16(body[pos : pos+2]))
		pos += 2
		if pos+nameLen > len(body) {
			break
		}
		name := decodeUTF16LE(body[pos : pos+nameLen])
		pos += nameLen

		if pos+4 > len(body) {
			break
		}
		valueType := binary.LittleEndian.Uint16(body[pos : pos+2])
		valueLen := int(binary.LittleEndian.Uint16(body[pos+2 : pos+4]))
		pos += 4
		if pos+valueLen > len(body) {
			break
		}
		valueBytes := body[pos : pos+valueLen]
		pos += valueLen

		switch strings.ToUpper(name) {
		case "WM/SHAREDUSERRATING":
			// WORD (type 5): same 0/25/50/75/99 breakpoints as the WMP POPM byte.
			if valueType == 5 && valueLen >= 2 {
				v := binary.LittleEndian.Uint16(valueBytes)
				if stars := popmWMPToStars(byte(v)); stars > 0 {
					found["WMP"] = stars
				}
			}
		case "FMPS_RATING":
			if valueType == 0 { // Unicode string
				s := strings.TrimSpace(decodeUTF16LE(valueBytes))
				if stars, ok := fmpsToStars(s); ok {
					found["MediaMonkey"] = stars
				}
			}
		}
	}

	for _, format := range tagOrder {
		if stars, ok := found[format]; ok {
			return stars, true
		}
	}
	return 0, false
}
