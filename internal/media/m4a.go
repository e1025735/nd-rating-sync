package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// errMalformedAtom reports an MP4 atom whose size does not fit its parent.
var errMalformedAtom = errors.New("MP4 atom size does not fit its parent")

// walkAtoms calls fn for each MP4 atom (box) in data, passing the 4-byte
// type string and the atom body (bytes after the size+type header). Return
// false from fn to stop early. An atom whose size does not fit data is
// errMalformedAtom; fewer than 8 trailing bytes (QuickTime ends some atom
// lists with a 4-byte zero terminator) are ignored.
func walkAtoms(data []byte, fn func(typ string, body []byte) bool) error {
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
				return errMalformedAtom
			}
			extSize := binary.BigEndian.Uint64(data[pos+8 : pos+16])
			if extSize < 16 || extSize > uint64(len(data)-pos) {
				return errMalformedAtom
			}
			headerLen = 16
			end = pos + int(extSize)
		default:
			if size < 8 || uint64(size) > uint64(len(data)-pos) {
				return errMalformedAtom
			}
			end = pos + int(size)
		}

		if !fn(typ, data[pos+headerLen:end]) {
			return nil
		}
		pos = end
	}
	return nil
}

// findAtom returns the body of the first typ atom in data, or nil when there
// is none.
func findAtom(data []byte, typ string) ([]byte, error) {
	var result []byte
	err := walkAtoms(data, func(t string, body []byte) bool {
		if t == typ {
			result = body
			return false
		}
		return true
	})
	return result, err
}

// metaChildren returns the child atoms of a meta atom. In MP4 files meta is
// a FullBox whose first 4 bytes are version+flags; QuickTime-style files
// write it as a plain atom whose first child (hdlr) starts right away.
func metaChildren(meta []byte) ([]byte, error) {
	if len(meta) >= 8 && string(meta[4:8]) == "hdlr" {
		return meta, nil
	}
	if len(meta) < 4 {
		return nil, errMalformedAtom
	}
	return meta[4:], nil
}

// ratingFromM4A walks the moov→udta→meta→ilst hierarchy and resolves a star
// rating from freeform (----) atoms. Recognised atom names:
//
//   - "FMPS_Rating" (any case) → FMPS and MediaMonkey, float 0.0–1.0
//   - "rating" (lowercase)     → iTunes-style (written by other tools; iTunes
//     itself keeps ratings in its library), integer 0/20/40/60/80/100
//   - "RATING" (uppercase)     → foobar2000, integer 1–5
//   - "RATE" (any case)        → MusicBee and MediaMonkey, integer 0/20/40/60/80/100
//
// For MediaMonkey, RATE (its own atom) wins over FMPS_Rating regardless of
// the order the atoms appear in ilst.
//
// A missing moov, udta, meta or ilst atom is untagged (the extractor only hands over a moov
// it walked cleanly); an atom whose size does not fit its parent anywhere on
// the way to the rating is an error, so the file is unreadable rather than
// untagged.
func ratingFromM4A(data []byte, path string, tagOrder []string) (int, bool, error) {
	moov, err := findAtom(data, "moov")
	if err != nil || moov == nil {
		return 0, false, err
	}
	udta, err := findAtom(moov, "udta")
	if err != nil || udta == nil {
		return 0, false, err
	}
	meta, err := findAtom(udta, "meta")
	if err != nil || meta == nil {
		return 0, false, err
	}
	children, err := metaChildren(meta)
	if err != nil {
		return 0, false, err
	}
	ilst, err := findAtom(children, "ilst")
	if err != nil || ilst == nil {
		return 0, false, err
	}

	r := newRatings(path)
	// RATE is MediaMonkey's own atom; it is applied after the walk (see below).
	rate, rateZero, hasRate := 0, true, false
	var innerErr error

	err = walkAtoms(ilst, func(typ string, body []byte) bool {
		if typ != "----" {
			return true
		}
		var name string
		var valueData []byte
		innerErr = walkAtoms(body, func(t string, b []byte) bool {
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
		if innerErr != nil {
			return false
		}

		value := strings.TrimSpace(string(valueData))
		nameUpper := strings.ToUpper(name)
		switch {
		case nameUpper == "FMPS_RATING":
			r.fmps(value)
		case name == "rating":
			// iTunes-style tools write lowercase "rating" on a 0–100 scale.
			stars, ok, err := ratingPercentToStars(value)
			r.parsed(stars, ok, err, "iTunes")
		case nameUpper == "RATING":
			// foobar2000 writes uppercase "RATING" with 1–5 integer scale.
			stars, _ := ratingIntToStars(value)
			r.decoded(stars, isZeroValue(value), "foobar2000")
		case nameUpper == "RATE":
			// MusicBee and MediaMonkey write "RATE" in MP4 containers with a 0–100 scale.
			rate, _ = ratingMusicBeeToStars(value)
			rateZero, hasRate = isZeroValue(value), true
			r.decoded(rate, rateZero, "MusicBee")
		}
		return true
	})
	if err == nil {
		err = innerErr
	}
	if err != nil {
		return 0, false, fmt.Errorf("MP4 tag list: %w", err)
	}
	// Set after the walk, so RATE wins over an FMPS_Rating wherever it is.
	if hasRate {
		r.decoded(rate, rateZero, "MediaMonkey")
	}
	return r.pick(tagOrder)
}

// errMP4CutOff is a short read inside the atom structure.
var errMP4CutOff = errors.New("MP4 file ends inside an atom header")

// readAtomHeader reads the header of the atom at pos, which must end by end
// (the end of the file or of the parent atom), and returns the atom's type,
// its whole size and the length of its header. A size of 0 means the atom
// runs to end (by the MP4 spec only the last atom does that); a size of 1
// means a 64-bit size follows the type. A size that does not fit before end
// is an error, as is a header cut off by the end of the file.
func readAtomHeader(f *os.File, pos, end int64) (typ string, size, headerLen int64, err error) {
	if _, err := f.Seek(pos, io.SeekStart); err != nil {
		return "", 0, 0, err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return "", 0, 0, cutOff(err, errMP4CutOff)
	}
	typ = string(hdr[4:8])
	headerLen = 8
	switch size32 := binary.BigEndian.Uint32(hdr[:4]); size32 {
	case 0:
		size = end - pos
	case 1:
		var ext [8]byte
		if _, err := io.ReadFull(f, ext[:]); err != nil {
			return "", 0, 0, cutOff(err, errMP4CutOff)
		}
		// A high-bit-set 64-bit size turns negative and fails below.
		size = int64(binary.BigEndian.Uint64(ext[:]))
		headerLen = 16
	default:
		size = int64(size32)
	}
	if size < headerLen || size > end-pos {
		return "", 0, 0, fmt.Errorf("MP4 atom %q at offset %d has size %d, which does not fit before offset %d", typ, pos, size, end)
	}
	return typ, size, headerLen, nil
}

// extractM4AMetadata walks the top-level MP4 atom list, Seeks past every
// non-moov atom (notably mdat, which holds the audio samples and can be
// many GiB), and returns a synthesised file containing only the moov atom
// with its udta child (see extractMoovUdta). ratingFromM4A
// walks the synth identically; findAtom finds "moov" first thing and
// descends into udta→meta→ilst.
//
// MP4 puts moov either at the start (fast-start muxing) or after mdat
// (default ffmpeg / iTunes layout). In the slow case we Seek over mdat
// without reading it — that single Seek is what makes large MP4s fast.
//
// Every MP4 file has a moov atom, so reaching the end of the file without
// one, an atom whose size does not fit the file, or a header cut off by the
// end of the file is an error: the file is unreadable, never untagged.
func extractM4AMetadata(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := info.Size()

	for pos, atoms := int64(0), 0; ; atoms++ {
		if pos >= fileSize {
			return nil, errors.New("MP4 file has no moov atom")
		}
		if atoms >= maxContainerEntries {
			return nil, errTooManyEntries
		}
		typ, atomSize, headerLen, err := readAtomHeader(f, pos, fileSize)
		if err != nil {
			return nil, err
		}
		if typ == "moov" {
			return extractMoovUdta(f, pos+headerLen, atomSize-headerLen)
		}
		// Skip the atom body without reading it (the big win for mdat).
		pos += atomSize
	}
}

// extractMoovUdta walks the children of a moov atom whose body starts at
// bodyStart and returns a synthesised moov atom holding only its udta child.
// The trak children carry the sample tables (stbl), which run to several MiB
// on long tracks; Seeking past them instead of reading the whole moov keeps a
// full sweep from exhausting the WASM heap. The udta body is read straight
// into the output buffer, so each file costs exactly one allocation.
//
// A moov walked to its end without a udta child holds no tags: (nil, nil),
// which the parser reports as untagged. A child whose size does not fit the
// moov is an error.
func extractMoovUdta(f *os.File, bodyStart, bodyLen int64) ([]byte, error) {
	end := bodyStart + bodyLen
	pos := bodyStart
	for atoms := 0; pos+8 <= end; atoms++ {
		if atoms >= maxContainerEntries {
			return nil, errTooManyEntries
		}
		typ, atomSize, headerLen, err := readAtomHeader(f, pos, end)
		if err != nil {
			return nil, err
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
