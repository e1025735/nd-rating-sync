package media

import (
	"bytes"
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
// On a missing/invalid header it returns (nil, nil); the caller hands the
// empty slice to parseID3v2Rating, which treats it as "no tag" (tagAbsent) —
// preserving the existing behaviour for files that look corrupt or untagged.
// An I/O error returns (nil, err) so the caller surfaces fileUnreadable and
// clear_rating_if_untagged cannot wipe a rating for a file we couldn't read.
func extractID3v2Metadata(f *os.File) ([]byte, error) {
	return readID3v2Tag(f, 0)
}

// readID3v2Tag reads one ID3v2 tag (header + body) from the current file
// position into a single buffer that starts with prefix reserved bytes, so a
// container (DSF) can write its own header there without copying the tag
// again. The returned slice holds prefix+10+tagSize bytes, or is nil when no
// valid tag is present.
func readID3v2Tag(f *os.File, prefix int) ([]byte, error) {
	var hdr [id3v2HeaderSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		return nil, err
	}
	if string(hdr[:3]) != "ID3" {
		return nil, nil
	}
	size := id3v2SyncsafeSize(hdr[6:10])
	if size < 0 || size > maxMetadataReadBytes {
		return nil, nil // bail to "no tag" rather than read pathological size
	}
	out := make([]byte, prefix+id3v2HeaderSize+size)
	copy(out[prefix:], hdr[:])
	if _, err := io.ReadFull(f, out[prefix+id3v2HeaderSize:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		return nil, err
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

// readID3v2TagAt seeks to off and reads one full ID3v2 tag (header + body)
// after prefix reserved bytes (see readID3v2Tag). Used by DSF (offset in the
// DSD header). Returns (nil, nil) when no valid tag is found at the offset.
func readID3v2TagAt(f *os.File, off int64, prefix int) ([]byte, error) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return readID3v2Tag(f, prefix)
}

// id3v2RatingFrames limits tag parsing to the frames that can carry a rating,
// so embedded cover art (APIC) and other large frames are skipped instead of
// being decoded into yet another copy.
var id3v2RatingFrames = []string{"TXXX", "POPM"}

// parseID3v2Rating inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating. It first collects every recognised rating
// frame from the file (one pass), then picks the winner by tagOrder.
//
// Recognised tagOrder values:
//   - "MediaMonkey" – POPM frame with email "no@email" (MediaMonkey's own
//     frame, exact whole-star bytes), else TXXX "FMPS_Rating" (float 0.0–1.0)
//   - "MusicBee"    – POPM frame with email "MusicBee" (exact whole-star bytes)
//   - "foobar2000"  – TXXX frame with description "RATING" (integer 1–5)
//   - "WMP"         – POPM frame written by Windows Media Player
//   - "iTunes"      – POPM frame written by iTunes / Apple Music
func parseID3v2Rating(data []byte, path string, tagOrder []string) (int, bool) {
	tag, err := id3v2.ParseReader(bytes.NewReader(data), id3v2.Options{Parse: true, ParseFrames: id3v2RatingFrames})
	if err != nil {
		return 0, false
	}
	defer tag.Close()

	found := make(map[string]int) // format → stars

	for _, f := range tag.GetFrames("TXXX") {
		txxx, ok := f.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}
		desc := strings.ToUpper(strings.TrimSpace(txxx.Description))
		switch desc {
		case "FMPS_RATING":
			if stars, ok := fmpsToStars(txxx.Value); ok {
				found["MediaMonkey"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for MediaMonkey for path %s", stars, path))
			}
		case "RATING":
			if stars, ok := ratingIntToStars(txxx.Value); ok {
				found["foobar2000"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for foobar2000 for path %s", stars, path))
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
			if stars := popmWMPToStars(popm.Rating); stars > 0 {
				found["WMP"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for WMP for path %s", stars, path))
			}
		case strings.Contains(e, "itunes") || "com.apple.itunes" == e:
			if stars := popmITunesToStars(popm.Rating); stars > 0 {
				found["iTunes"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for iTunes for path %s", stars, path))
			}
		case "musicbee" == e:
			if stars := popmMusicBeeToStars(popm.Rating); stars > 0 {
				found["MusicBee"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for MusicBee for path %s", stars, path))
			}
		case "no@email" == e:
			// MediaMonkey's own rating frame. It runs after the TXXX loop, so it
			// takes precedence over an FMPS_Rating another app may have written.
			if stars := popmMediaMonkeyToStars(popm.Rating); stars > 0 {
				found["MediaMonkey"] = stars
				logTrace(fmt.Sprintf("nd-rating-sync: found rating of \"%q\" for MediaMonkey for path %s", stars, path))
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
