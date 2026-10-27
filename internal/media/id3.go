package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// id3v2HeaderSize is the fixed ID3v2 tag header length.
const id3v2HeaderSize = 10

// extractID3v2Metadata reads only the ID3v2 tag from an MP3 file: the 10-byte
// header carries the tag size in a syncsafe field, so we can read exactly
// (header + tagSize) bytes and stop — never touching the MP3 audio frames.
//
// A file without the "ID3" magic returns (nil, nil); the caller hands the
// empty slice to the parser, which reports "no tag" (tagAbsent): an MP3
// without a leading ID3v2 tag carries no rating any supported player writes.
// A file too short for the 10-byte header, a tag that starts but is corrupt
// (invalid size field), larger than maxMetadataReadBytes, or cut off returns
// an error, as does any I/O error, so the caller reports fileUnreadable and
// clear_rating_if_untagged cannot wipe a rating for a tag we could not read.
func extractID3v2Metadata(f *os.File) ([]byte, error) {
	return readID3v2Tag(f, 0)
}

// readID3v2Tag reads one ID3v2 tag (header + body) from the current file
// position into a single buffer that starts with prefix reserved bytes, so a
// container (DSF) can write its own header there without copying the tag
// again. The returned slice holds prefix+10+tagSize bytes, or is nil when the
// "ID3" magic is absent.
func readID3v2Tag(f *os.File, prefix int) ([]byte, error) {
	var hdr [id3v2HeaderSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, cutOff(err, errors.New("file ends before a complete ID3v2 header"))
	}
	if string(hdr[:3]) != "ID3" {
		return nil, nil
	}
	size := id3v2SyncsafeSize(hdr[6:10])
	if size < 0 {
		return nil, errors.New("ID3v2 tag size is not a valid syncsafe integer")
	}
	if size > maxMetadataReadBytes {
		return nil, fmt.Errorf("ID3v2 tag size %d exceeds cap %d", size, maxMetadataReadBytes)
	}
	out := make([]byte, prefix+id3v2HeaderSize+size)
	copy(out[prefix:], hdr[:])
	if _, err := io.ReadFull(f, out[prefix+id3v2HeaderSize:]); err != nil {
		return nil, cutOff(err, errors.New("ID3v2 tag is cut off before its declared end"))
	}
	return out, nil
}

// id3v2SyncsafeSize decodes the 28-bit syncsafe integer used for ID3v2 tag
// size: each byte contributes only its lower 7 bits. Returns -1 if the top
// bit of any byte is set (not strictly syncsafe).
func id3v2SyncsafeSize(b []byte) int {
	if len(b) != 4 {
		return -1
	}
	for _, x := range b {
		if x&0x80 != 0 {
			return -1
		}
	}
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3])
}

// id3v2FooterFlag marks an ID3v2.4 tag followed by a 10-byte footer that its
// size field does not include.
const id3v2FooterFlag = 0x10

// skipLeadingID3v2 moves f past an ID3v2 tag at the start of the file. Some
// taggers put one in front of a FLAC stream; libFLAC skips it, and so does
// the FLAC extractor. A broken or cut-off tag is an error.
func skipLeadingID3v2(f *os.File) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var hdr [id3v2HeaderSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return cutOff(err, errors.New("file ends inside a leading ID3v2 header"))
	}
	size := id3v2SyncsafeSize(hdr[6:10])
	if string(hdr[:3]) != "ID3" || size < 0 {
		return errors.New("leading ID3v2 tag has a broken header")
	}
	skip := int64(id3v2HeaderSize + size)
	if hdr[3] == 4 && hdr[5]&id3v2FooterFlag != 0 {
		skip += id3v2HeaderSize
	}
	_, err := f.Seek(skip, io.SeekStart)
	return err
}

// readID3v2TagAt seeks to off and reads one full ID3v2 tag (header + body)
// after prefix reserved bytes (see readID3v2Tag). Used by DSF (offset in the
// DSD header). Returns (nil, nil) when no valid tag is found at the offset.
func readID3v2TagAt(f *os.File, off int64, prefix int) ([]byte, error) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return readID3v2Tag(f, prefix)
}

// ID3v2 header and frame flags the tag library does not implement.
const (
	id3v2ExtendedHeaderFlag = 0x40

	// v2.3 frame format flags (second flag byte).
	id3v23FrameCompressed = 0x80
	id3v23FrameEncrypted  = 0x40
	id3v23FrameGrouped    = 0x20

	// v2.4 frame format flags (second flag byte).
	id3v24FrameGrouped    = 0x40
	id3v24FrameCompressed = 0x08
	id3v24FrameEncrypted  = 0x04
	id3v24FrameDataLength = 0x01
)

// prepareID3v2 works around ID3v2 features bogem/id3v2 does not implement,
// before the tag is handed to it:
//
//   - an extended header (v2.3/v2.4) is cut out and the header flag cleared;
//     the library would read it as frame data and, for v2.3, silently find
//     no frames at all;
//   - a POPM or TXXX frame that is compressed, encrypted, grouped or carries
//     a data-length indicator would be misread as "no rating", so it is an
//     error and the file is unreadable rather than untagged.
//
// Data without the "ID3" magic, other versions and anything the walk does
// not understand are returned unchanged for the library to judge.
func prepareID3v2(data []byte) ([]byte, error) {
	if len(data) < id3v2HeaderSize || string(data[:3]) != "ID3" {
		return data, nil
	}
	version, flags := data[3], data[5]
	if version != 3 && version != 4 {
		return data, nil
	}
	size := id3v2SyncsafeSize(data[6:10])
	if size < 0 || size > len(data)-id3v2HeaderSize {
		return data, nil
	}
	frames := data[id3v2HeaderSize : id3v2HeaderSize+size]

	if flags&id3v2ExtendedHeaderFlag != 0 {
		if len(frames) < 4 {
			return nil, errors.New("ID3v2 extended header is cut off")
		}
		var extLen int
		if version == 3 {
			// v2.3: the size excludes its own 4 bytes.
			extLen = 4 + int(binary.BigEndian.Uint32(frames[:4])&0x7FFFFFFF)
		} else {
			// v2.4: a syncsafe size that includes itself.
			extLen = id3v2SyncsafeSize(frames[:4])
		}
		if extLen < 4 || extLen > len(frames) {
			return nil, errors.New("ID3v2 extended header does not fit the tag")
		}
		frames = frames[extLen:]
		rebuilt := make([]byte, id3v2HeaderSize+len(frames))
		copy(rebuilt, data[:id3v2HeaderSize])
		rebuilt[5] = flags &^ id3v2ExtendedHeaderFlag
		n := len(frames)
		rebuilt[6], rebuilt[7], rebuilt[8], rebuilt[9] = byte(n>>21&0x7F), byte(n>>14&0x7F), byte(n>>7&0x7F), byte(n&0x7F)
		copy(rebuilt[id3v2HeaderSize:], frames)
		data = rebuilt
	}

	for pos := 0; pos+id3v2HeaderSize <= len(frames); {
		id := string(frames[pos : pos+4])
		if frames[pos] == 0 {
			break // padding
		}
		var frameSize int
		if version == 3 {
			frameSize = int(binary.BigEndian.Uint32(frames[pos+4:pos+8]) & 0x7FFFFFFF)
		} else {
			frameSize = id3v2SyncsafeSize(frames[pos+4 : pos+8])
		}
		if frameSize < 0 || frameSize > len(frames)-pos-id3v2HeaderSize {
			break // the library reports a broken frame itself
		}
		format := frames[pos+9]
		var unsupported bool
		if version == 3 {
			unsupported = format&(id3v23FrameCompressed|id3v23FrameEncrypted|id3v23FrameGrouped) != 0
		} else {
			unsupported = format&(id3v24FrameGrouped|id3v24FrameCompressed|id3v24FrameEncrypted|id3v24FrameDataLength) != 0
		}
		if unsupported && (id == "POPM" || id == "TXXX") {
			return nil, fmt.Errorf("ID3v2 %s frame is compressed, encrypted, grouped or has a data-length indicator (flags 0x%02x), which is not supported", id, format)
		}
		pos += id3v2HeaderSize + frameSize
	}
	return data, nil
}

// id3v2RatingFrames limits tag parsing to the frames that can carry a rating,
// so embedded cover art (APIC) and other large frames are skipped instead of
// being decoded into yet another copy.
var id3v2RatingFrames = []string{"TXXX", "POPM"}

// ratingFromID3v2 inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating. It first collects every recognised rating
// frame from the file (one pass), then picks the winner by tagOrder. Empty
// data (no "ID3" magic) is untagged; a corrupt or unsupported tag (e.g.
// ID3v2.2) is an error, so the file is unreadable rather than untagged.
//
// Recognised tagOrder values:
//   - "MediaMonkey" – POPM frame with email "no@email" (MediaMonkey's own
//     frame, exact whole-star bytes), else TXXX "FMPS_Rating" (float 0.0–1.0)
//   - "MusicBee"    – POPM frame with email "MusicBee" (exact whole-star bytes)
//   - "foobar2000"  – TXXX frame with description "RATING" (integer 1–5),
//     else the "Windows Media Player" POPM frame foobar2000 writes to MP3
//   - "WMP"         – POPM frame written by Windows Media Player
//   - "iTunes"      – POPM frame with an iTunes email, written by other tools
//     (iTunes itself keeps ratings in its library, not in the file)
//   - "FMPS"        – TXXX "FMPS_Rating" (Strawberry, Clementine, Amarok, …)
func ratingFromID3v2(data []byte, path string, tagOrder []string) (int, bool, error) {
	data, err := prepareID3v2(data)
	if err != nil {
		return 0, false, err
	}
	tag, err := id3v2.ParseReader(bytes.NewReader(data), id3v2.Options{Parse: true, ParseFrames: id3v2RatingFrames})
	if err != nil {
		return 0, false, fmt.Errorf("parse ID3v2 tag: %w", err)
	}
	defer tag.Close()

	r := newRatings(path)
	for _, f := range tag.GetFrames("TXXX") {
		txxx, ok := f.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(txxx.Description)) {
		case "FMPS_RATING":
			r.fmps(txxx.Value)
		case "RATING":
			if stars, ok := ratingIntToStars(txxx.Value); ok {
				r.set(stars, "foobar2000")
			}
		}
	}

	for _, f := range tag.GetFrames("POPM") {
		popm, ok := f.(id3v2.PopularimeterFrame)
		if !ok {
			continue
		}
		e := strings.ToLower(strings.TrimSpace(popm.Email))
		switch {
		case strings.Contains(e, "windows media player"):
			stars := popmWMPToStars(popm.Rating)
			r.set(stars, "WMP")
			// foobar2000 writes its RATING field to MP3 as this frame. It
			// runs after the TXXX loop, so a TXXX "RATING" wins.
			if !r.has("foobar2000") {
				r.set(stars, "foobar2000")
			}
		case strings.Contains(e, "itunes") || "com.apple.itunes" == e:
			r.set(popmITunesToStars(popm.Rating), "iTunes")
		case "musicbee" == e:
			r.set(popmMusicBeeToStars(popm.Rating), "MusicBee")
		case "no@email" == e:
			// MediaMonkey's own rating frame. It runs after the TXXX loop, so it
			// takes precedence over an FMPS_Rating another app may have written.
			r.set(popmMediaMonkeyToStars(popm.Rating), "MediaMonkey")
		}
	}

	return r.pick(tagOrder)
}
