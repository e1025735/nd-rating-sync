package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
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

// asfHeaderObjectFixedSize is the fixed 30-byte ASF Header Object preamble
// (16 GUID + 8 size + 4 numHeaders + 2 reserved).
const asfHeaderObjectFixedSize = 30

// asfObjectHeaderSize is the per-child-object header (16 GUID + 8 size).
const asfObjectHeaderSize = 24

// extractWMAMetadata reads the ASF Header Object, walks its child objects
// (Seeking past every one that isn't the Extended Content Description
// Object), and returns a synthesised ASF Header containing only the ECDO.
// Audio data (which lives in a separate top-level ASF Data Object after the
// Header Object — potentially hundreds of MiB) is never read.
//
// parseWMARating walks this synth identically to a real file: it sees a
// Header Object with numHeaders=1 and the ECDO as the only child.
func extractWMAMetadata(f *os.File) ([]byte, error) {
	var hdr [asfHeaderObjectFixedSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		return nil, err
	}
	if !bytes.Equal(hdr[:16], asfHeaderObjectGUID) {
		return nil, nil
	}
	numHeaders := binary.LittleEndian.Uint32(hdr[24:28])

	for i := uint32(0); i < numHeaders; i++ {
		var objHdr [asfObjectHeaderSize]byte
		if _, err := io.ReadFull(f, objHdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return wmaSynthEmpty(), nil
			}
			return nil, err
		}
		objSize := int64(binary.LittleEndian.Uint64(objHdr[16:24]))
		if objSize < asfObjectHeaderSize {
			return wmaSynthEmpty(), nil
		}
		bodyLen := objSize - asfObjectHeaderSize

		if bytes.Equal(objHdr[:16], asfExtContentDescObjectGUID) {
			if bodyLen > maxMetadataReadBytes {
				return nil, fmt.Errorf("WMA ECDO size %d exceeds cap %d", bodyLen, maxMetadataReadBytes)
			}
			body := make([]byte, bodyLen)
			if _, err := io.ReadFull(f, body); err != nil {
				return nil, err
			}
			// Synth: ASF Header (numHeaders=1, adjusted size) + ECDO only.
			totalSize := int64(asfHeaderObjectFixedSize) + objSize
			out := make([]byte, 0, totalSize)
			out = append(out, asfHeaderObjectGUID...)
			var sizeBuf [8]byte
			binary.LittleEndian.PutUint64(sizeBuf[:], uint64(totalSize))
			out = append(out, sizeBuf[:]...)
			var numBuf [4]byte
			binary.LittleEndian.PutUint32(numBuf[:], 1) // exactly one child
			out = append(out, numBuf[:]...)
			out = append(out, hdr[28], hdr[29]) // preserve the two reserved bytes
			out = append(out, objHdr[:]...)     // ECDO header
			out = append(out, body...)
			return out, nil
		}

		// Skip non-ECDO object body without reading it.
		if _, err := f.Seek(bodyLen, io.SeekCurrent); err != nil {
			return wmaSynthEmpty(), nil
		}
	}

	return wmaSynthEmpty(), nil
}

// wmaSynthEmpty returns a minimal valid ASF Header Object with numHeaders=0.
// parseWMARating walks zero child objects and reports tagAbsent.
func wmaSynthEmpty() []byte {
	out := make([]byte, asfHeaderObjectFixedSize)
	copy(out[:16], asfHeaderObjectGUID)
	binary.LittleEndian.PutUint64(out[16:24], asfHeaderObjectFixedSize)
	binary.LittleEndian.PutUint32(out[24:28], 0) // no children
	// bytes 28..29 reserved, leave zero
	return out
}
