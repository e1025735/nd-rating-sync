package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ASF descriptor value types (Extended Content Description Object).
const (
	asfTypeDWORD = 3
	asfTypeWORD  = 5
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

// errMalformedASF reports an ASF object or descriptor whose size does not fit
// the data around it.
var errMalformedASF = errors.New("ASF header object is malformed")

// ratingFromWMA walks the ASF Header Object looking for an Extended Content
// Description Object and extracts a star rating from it. Empty data and a
// header without an Extended Content Description Object are untagged; a
// header, child object or descriptor whose size does not fit is an error, so
// the file is unreadable rather than untagged. Sizes are compared as uint64
// before narrowing, so a high-bit-set size cannot wrap on 32-bit wasip1.
func ratingFromWMA(data []byte, path string, tagOrder []string) (int, bool, error) {
	if len(data) == 0 {
		return 0, false, nil
	}
	// ASF Header Object layout: 16-byte GUID + 8-byte size + 4-byte num child
	// objects + 2 reserved bytes = 30 bytes fixed.
	if len(data) < asfHeaderObjectFixedSize || !bytes.Equal(data[:16], asfHeaderObjectGUID) {
		return 0, false, errors.New("not an ASF header object")
	}
	numHeaders := binary.LittleEndian.Uint32(data[24:28])
	pos := asfHeaderObjectFixedSize

	for i := uint32(0); i < numHeaders; i++ {
		if len(data)-pos < asfObjectHeaderSize {
			return 0, false, errMalformedASF
		}
		guid := data[pos : pos+16]
		objSize := binary.LittleEndian.Uint64(data[pos+16 : pos+24])
		if objSize < asfObjectHeaderSize || objSize > uint64(len(data)-pos) {
			return 0, false, errMalformedASF
		}
		end := pos + int(objSize)
		if bytes.Equal(guid, asfExtContentDescObjectGUID) {
			return parseASFExtContentDesc(data[pos+asfObjectHeaderSize:end], path, tagOrder)
		}
		pos = end
	}
	return 0, false, nil
}

// parseASFExtContentDesc reads the body of an ASF Extended Content Description
// Object (after its 24-byte GUID+size header) and extracts a star rating.
// Recognised descriptors:
//
//   - "WM/SharedUserRating" (DWORD/type 3, or WORD/type 5): WMP 0–99 scale
//   - "FMPS_Rating"         (Unicode/type 0): FMPS and MediaMonkey, float 0.0–1.0
//
// A descriptor that runs past the object is an error.
func parseASFExtContentDesc(body []byte, path string, tagOrder []string) (int, bool, error) {
	if len(body) < 2 {
		return 0, false, errMalformedASF
	}
	count := int(binary.LittleEndian.Uint16(body[:2]))
	pos := 2
	r := newRatings(path)

	for i := 0; i < count; i++ {
		if pos+2 > len(body) {
			return 0, false, errMalformedASF
		}
		nameLen := int(binary.LittleEndian.Uint16(body[pos : pos+2]))
		pos += 2
		if pos+nameLen > len(body) {
			return 0, false, errMalformedASF
		}
		name := decodeUTF16LE(body[pos : pos+nameLen])
		pos += nameLen

		if pos+4 > len(body) {
			return 0, false, errMalformedASF
		}
		valueType := binary.LittleEndian.Uint16(body[pos : pos+2])
		valueLen := int(binary.LittleEndian.Uint16(body[pos+2 : pos+4]))
		pos += 4
		if pos+valueLen > len(body) {
			return 0, false, errMalformedASF
		}
		valueBytes := body[pos : pos+valueLen]
		pos += valueLen

		switch strings.ToUpper(name) {
		case "WM/SHAREDUSERRATING":
			// The Windows Media Format SDK defines this as a DWORD (type 3) on a
			// 0–99 scale (WMP writes 1/25/50/75/99). WORD (type 5) is accepted
			// too for taggers that write the narrower type.
			var v uint32
			switch {
			case valueType == asfTypeDWORD && valueLen >= 4:
				v = binary.LittleEndian.Uint32(valueBytes[:4])
			case valueType == asfTypeWORD && valueLen >= 2:
				v = uint32(binary.LittleEndian.Uint16(valueBytes[:2]))
			}
			r.decoded(sharedUserRatingToStars(v), v == 0, "WMP", "MusicBee")
		case "FMPS_RATING":
			if valueType == 0 { // Unicode string
				r.fmps(decodeUTF16LE(valueBytes))
			}
		}
	}

	return r.pick(tagOrder)
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
// ratingFromWMA walks this synth identically to a real file: it sees a
// Header Object with numHeaders=1 and the ECDO as the only child.
//
// Only a header whose children were all walked without an ECDO is untagged
// (an empty synth). A file without the ASF header GUID, a header that does
// not fit the file, or a child object that is cut off or does not fit the
// header is an error: the file is unreadable, never untagged.
func extractWMAMetadata(f *os.File) ([]byte, error) {
	var hdr [asfHeaderObjectFixedSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, cutOff(err, errors.New("file ends inside the ASF header"))
	}
	if !bytes.Equal(hdr[:16], asfHeaderObjectGUID) {
		return nil, errors.New("not an ASF file (header object GUID missing)")
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	headerSize := binary.LittleEndian.Uint64(hdr[16:24])
	if headerSize < asfHeaderObjectFixedSize || headerSize > uint64(info.Size()) {
		return nil, fmt.Errorf("ASF header size %d does not fit the %d-byte file", headerSize, info.Size())
	}
	headerEnd := int64(headerSize)
	numHeaders := binary.LittleEndian.Uint32(hdr[24:28])
	if numHeaders > maxContainerEntries {
		return nil, errTooManyEntries
	}

	pos := int64(asfHeaderObjectFixedSize)
	for i := uint32(0); i < numHeaders; i++ {
		var objHdr [asfObjectHeaderSize]byte
		if _, err := io.ReadFull(f, objHdr[:]); err != nil {
			return nil, cutOff(err, errors.New("file ends inside the ASF header"))
		}
		objSize := binary.LittleEndian.Uint64(objHdr[16:24])
		if objSize < asfObjectHeaderSize || objSize > uint64(headerEnd-pos) {
			return nil, fmt.Errorf("ASF header object %d has size %d, which does not fit the header", i, objSize)
		}
		bodyLen := int64(objSize) - asfObjectHeaderSize

		if bytes.Equal(objHdr[:16], asfExtContentDescObjectGUID) {
			if bodyLen > maxMetadataReadBytes {
				return nil, fmt.Errorf("WMA ECDO size %d exceeds cap %d", bodyLen, maxMetadataReadBytes)
			}
			// Synth: ASF Header (numHeaders=1, adjusted size) + ECDO only,
			// with the ECDO body read straight into it (one allocation).
			totalSize := int64(asfHeaderObjectFixedSize) + int64(objSize)
			out := make([]byte, totalSize)
			copy(out[:16], asfHeaderObjectGUID)
			binary.LittleEndian.PutUint64(out[16:24], uint64(totalSize))
			binary.LittleEndian.PutUint32(out[24:28], 1) // exactly one child
			out[28], out[29] = hdr[28], hdr[29]          // preserve the two reserved bytes
			copy(out[30:54], objHdr[:])                  // ECDO header
			if _, err := io.ReadFull(f, out[54:]); err != nil {
				return nil, err
			}
			return out, nil
		}

		// Skip non-ECDO object body without reading it.
		pos += int64(objSize)
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
	}

	return wmaSynthEmpty(), nil
}

// wmaSynthEmpty returns a minimal valid ASF Header Object with numHeaders=0.
// ratingFromWMA walks zero child objects and reports tagAbsent.
func wmaSynthEmpty() []byte {
	out := make([]byte, asfHeaderObjectFixedSize)
	copy(out[:16], asfHeaderObjectGUID)
	binary.LittleEndian.PutUint64(out[16:24], asfHeaderObjectFixedSize)
	binary.LittleEndian.PutUint32(out[24:28], 0) // no children
	// bytes 28..29 reserved, leave zero
	return out
}
