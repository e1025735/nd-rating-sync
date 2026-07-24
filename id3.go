package main

import (
	"bytes"
	"errors"
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
	body := make([]byte, size)
	if _, err := io.ReadFull(f, body); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]byte, 0, id3v2HeaderSize+size)
	out = append(out, hdr[:]...)
	out = append(out, body...)
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

// readID3v2TagAt seeks to off and reads one full ID3v2 tag (header + body).
// Used by WAV (id3 RIFF chunk delegation) and DSF (offset in DSD header).
// Returns (nil, nil) when no valid tag is found at the offset.
func readID3v2TagAt(f *os.File, off int64) ([]byte, error) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return extractID3v2Metadata(f)
}

// parseID3v2Rating inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating. It first collects every recognised rating
// frame from the file (one pass), then picks the winner by tagOrder.
//
// Recognised tagOrder values:
//   - "MediaMonkey" – TXXX frame with description "FMPS_Rating" (float 0.0–1.0)
//   - "foobar2000"  – TXXX frame with description "RATING" (integer 1–5)
//   - "WMP"         – POPM frame written by Windows Media Player
//   - "iTunes"      – POPM frame written by iTunes / Apple Music
func parseID3v2Rating(data []byte, tagOrder []string) (int, bool) {
	tag, err := id3v2.ParseReader(bytes.NewReader(data), id3v2.Options{Parse: true})
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
			}
		case "RATING":
			if stars, ok := ratingIntToStars(txxx.Value); ok {
				found["foobar2000"] = stars
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
			}
		case strings.Contains(e, "itunes") || "com.apple.itunes" == e:
			if stars := popmITunesToStars(popm.Rating); stars > 0 {
				found["iTunes"] = stars
			}
		case "musicbee" == e:
			if stars := popmWMPToStars(popm.Rating); stars > 0 {
				found["MusicBee"] = stars
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
