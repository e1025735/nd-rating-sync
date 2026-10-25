package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
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
//   - "RATE" (any case)        → MusicBee, integer 0/20/40/60/80/100
func parseM4ARating(data []byte, path string, tagOrder []string) (int, bool) {
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
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for MediaMonkey for path %s", stars, path))
			}
		case name == "rating":
			// iTunes writes lowercase "rating" with 0/20/40/60/80/100 integer scale.
			var n int
			if _, err := fmt.Sscanf(value, "%d", &n); err == nil && n > 0 {
				if stars := popmITunesToStars(byte(n)); stars > 0 {
					found["iTunes"] = stars
					logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for iTunes for path %s", stars, path))
				}
			}
		case nameUpper == "RATING":
			// foobar2000 writes uppercase "RATING" with 1–5 integer scale.
			if stars, ok := ratingIntToStars(value); ok {
				found["foobar2000"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for foobar2000 for path %s", stars, path))
			}
		case nameUpper == "RATE":
			// MusicBee writes "RATE" in MP4 containers with a 0–100 scale.
			if stars, ok := ratingMusicBeeToStars(value); ok {
				found["MusicBee"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for MusicBee for path %s", stars, path))
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

// extractM4AMetadata walks the top-level MP4 atom list, Seeks past every
// non-moov atom (notably mdat, which holds the audio samples and can be
// many GiB), and returns a synthesised file containing only the moov atom
// with its udta child (see extractMoovUdta). The existing parseM4ARating
// walks the synth identically; findAtom finds "moov" first thing and
// descends into udta→meta→ilst.
//
// MP4 puts moov either at the start (fast-start muxing) or after mdat
// (default ffmpeg / iTunes layout). In the slow case we Seek over mdat
// without reading it — that single Seek is what makes large MP4s fast.
func extractM4AMetadata(f *os.File) ([]byte, error) {
	pos := int64(0)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, nil
			}
			return nil, err
		}
		size32 := binary.BigEndian.Uint32(hdr[:4])
		typ := string(hdr[4:8])
		var atomSize int64
		var headerLen int64 = 8

		switch size32 {
		case 0:
			// size32 == 0 means "this atom extends to EOF" — by the MP4
			// spec it's always the last atom in the file. Compute the
			// length via Seek-to-end; the loop will hit EOF on the next
			// header read and return cleanly.
			end, err := f.Seek(0, io.SeekEnd)
			if err != nil {
				return nil, err
			}
			atomSize = end - pos
			// Position back to right after the header in case typ == "moov"
			// and we need to read the body next.
			if _, err := f.Seek(pos+headerLen, io.SeekStart); err != nil {
				return nil, err
			}
		case 1:
			var ext [8]byte
			if _, err := io.ReadFull(f, ext[:]); err != nil {
				return nil, err
			}
			atomSize = int64(binary.BigEndian.Uint64(ext[:]))
			headerLen = 16
			if atomSize < headerLen {
				return nil, nil
			}
		default:
			atomSize = int64(size32)
			if atomSize < headerLen {
				return nil, nil
			}
		}

		if typ == "moov" {
			bodyLen := atomSize - headerLen
			if bodyLen < 0 {
				return nil, nil
			}
			return extractMoovUdta(f, pos+headerLen, bodyLen)
		}

		// Skip non-moov atom body without reading it (this is the big win
		// for mdat, which is the audio data — often hundreds of MiB).
		if _, err := f.Seek(pos+atomSize, io.SeekStart); err != nil {
			// Truncated file or unseekable past EOF; treat as "no moov".
			return nil, nil
		}
		pos += atomSize
	}
}

// extractMoovUdta walks the children of a moov atom whose body starts at
// bodyStart and returns a synthesised moov atom holding only its udta child.
// The trak children carry the sample tables (stbl), which run to several MiB
// on long tracks; Seeking past them instead of reading the whole moov keeps a
// full sweep from exhausting the WASM heap. The udta body is read straight
// into the output buffer, so each file costs exactly one allocation.
func extractMoovUdta(f *os.File, bodyStart, bodyLen int64) ([]byte, error) {
	end := bodyStart + bodyLen
	pos := bodyStart
	for pos+8 <= end {
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, nil
		}
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, nil
			}
			return nil, err
		}
		size32 := binary.BigEndian.Uint32(hdr[:4])
		typ := string(hdr[4:8])
		var atomSize int64
		var headerLen int64 = 8

		switch size32 {
		case 0:
			// Extends to the end of the enclosing moov.
			atomSize = end - pos
		case 1:
			if pos+16 > end {
				return nil, nil
			}
			var ext [8]byte
			if _, err := io.ReadFull(f, ext[:]); err != nil {
				return nil, err
			}
			atomSize = int64(binary.BigEndian.Uint64(ext[:]))
			headerLen = 16
		default:
			atomSize = int64(size32)
		}
		// A negative atomSize (high-bit-set 64-bit size) also fails here.
		if atomSize < headerLen || atomSize > end-pos {
			return nil, nil
		}

		if typ == "udta" {
			udtaLen := atomSize - headerLen
			if udtaLen > maxMetadataReadBytes {
				return nil, fmt.Errorf("MP4 udta atom size %d exceeds cap %d", udtaLen, maxMetadataReadBytes)
			}
			out := make([]byte, 16+udtaLen)
			binary.BigEndian.PutUint32(out[0:4], uint32(16+udtaLen))
			copy(out[4:8], "moov")
			binary.BigEndian.PutUint32(out[8:12], uint32(8+udtaLen))
			copy(out[12:16], "udta")
			if _, err := io.ReadFull(f, out[16:]); err != nil {
				return nil, err
			}
			return out, nil
		}
		pos += atomSize
	}
	return nil, nil
}
