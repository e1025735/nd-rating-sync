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

// FLAC metadata block types (FLAC spec §4.2). Only VORBIS_COMMENT is used.
const flacBlockVorbisComment = 4

// vorbisComments is a multimap of Vorbis comment fields. Keys are upper-cased
// because the Vorbis spec defines field names as case-insensitive ASCII.
// Values are kept in order; some fields (e.g. ARTIST) may legally repeat.
type vorbisComments map[string][]string

// parseFLACVorbisComments walks a FLAC stream's metadata blocks and returns
// the contents of its VORBIS_COMMENT block, or an empty map if the file has
// none. Audio frames after the metadata are not read.
func parseFLACVorbisComments(data []byte) (vorbisComments, error) {
	if len(data) < 4 || string(data[:4]) != "fLaC" {
		return nil, errors.New("not a FLAC file (missing fLaC magic)")
	}
	pos := 4
	for {
		if len(data)-pos < flacMetadataBlockHeaderSize {
			return nil, fmt.Errorf("metadata block header: %w", io.ErrUnexpectedEOF)
		}
		hdr := data[pos : pos+flacMetadataBlockHeaderSize]
		pos += flacMetadataBlockHeaderSize
		isLast := hdr[0]&0x80 != 0
		blockType := hdr[0] & 0x7F
		blockLen := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])

		if blockLen > len(data)-pos {
			return nil, errors.New("metadata block extends past end of file")
		}
		// Sub-slice rather than copy: the block body is only read from.
		body := data[pos : pos+blockLen]
		pos += blockLen

		if blockType == flacBlockVorbisComment {
			return parseVorbisCommentBlock(body)
		}
		if isLast {
			return vorbisComments{}, nil
		}
	}
}

// vorbisPictureKey is the Vorbis comment holding base64-encoded cover art
// (common in Ogg/Opus, often hundreds of KiB). It can never carry a rating, so
// parseVorbisCommentBlock does not copy it into the result.
const vorbisPictureKey = "METADATA_BLOCK_PICTURE"

// parseVorbisCommentBlock decodes the body of a VORBIS_COMMENT metadata block.
// Layout: vendor_length (u32 LE) + vendor + count (u32 LE) + count×(len + "KEY=value").
//
// Lengths are compared as uint64: on 32-bit wasip1 a length >= 2^31 would
// wrap negative as an int, pass the bounds check and trigger a multi-GiB
// allocation that kills the whole plugin. Entries are sub-slices of body, so
// only the stored key/value strings are copied.
func parseVorbisCommentBlock(body []byte) (vorbisComments, error) {
	out := vorbisComments{}
	pos := 0

	readUint32 := func() (uint32, error) {
		if len(body)-pos < 4 {
			return 0, io.ErrUnexpectedEOF
		}
		n := binary.LittleEndian.Uint32(body[pos : pos+4])
		pos += 4
		return n, nil
	}
	readString := func() ([]byte, error) {
		n, err := readUint32()
		if err != nil {
			return nil, err
		}
		if uint64(n) > uint64(len(body)-pos) {
			return nil, fmt.Errorf("string length %d exceeds remaining %d", n, len(body)-pos)
		}
		buf := body[pos : pos+int(n)]
		pos += int(n)
		return buf, nil
	}

	if _, err := readString(); err != nil {
		return nil, fmt.Errorf("vendor string: %w", err)
	}

	count, err := readUint32()
	if err != nil {
		return nil, fmt.Errorf("comment count: %w", err)
	}
	// Each entry needs at least its 4-byte length, so a corrupt count fails
	// on the bounds check long before it could loop for long.
	for i := uint32(0); i < count; i++ {
		buf, err := readString()
		if err != nil {
			return nil, fmt.Errorf("comment %d: %w", i, err)
		}
		eq := bytes.IndexByte(buf, '=')
		if eq < 0 {
			continue // malformed entry, ignore
		}
		key := strings.ToUpper(string(buf[:eq]))
		if key == vorbisPictureKey {
			continue
		}
		out[key] = append(out[key], string(buf[eq+1:]))
	}
	return out, nil
}

// ratingFromVorbisComments resolves a star rating from a Vorbis comment map
// using tagOrder priority. Shared by the FLAC and Ogg/Opus paths. Recognised
// source applications:
//
//   - "MediaMonkey" – RATING (0–100, MediaMonkey's own field), else FMPS_RATING (float 0.0–1.0)
//   - "foobar2000"  – RATING (integer 1–5)
//   - "MusicBee"    – RATING (0–100)
//   - "FMPS"        – FMPS_RATING (float 0.0–1.0; Strawberry, Clementine, Amarok, …)
//
// The 1–5 and 0–100 scales never overlap (0–100 values are multiples of 20),
// so one RATING value matches either foobar2000 or MusicBee/MediaMonkey.
//
// "WMP" and "iTunes" have no canonical Vorbis representation and are silently
// skipped — listing them in tagOrder is harmless, they just never match.
func ratingFromVorbisComments(cmts vorbisComments, path string, tagOrder []string) (int, bool, error) {
	found := map[string]int{}
	invalid := map[string]error{}
	if vs := cmts["FMPS_RATING"]; len(vs) > 0 {
		stars, ok, err := fmpsToStars(vs[0])
		switch {
		case err != nil:
			invalid["FMPS"] = err
			invalid["MediaMonkey"] = err
		case ok:
			found["FMPS"] = stars
			found["MediaMonkey"] = stars
			logTrace(fmt.Sprintf("nd-rating-sync: found %d stars for FMPS and MediaMonkey in %q", stars, path))
		}
	}
	if vs := cmts["RATING"]; len(vs) > 0 {
		if stars, ok := ratingIntToStars(vs[0]); ok {
			found["foobar2000"] = stars
			logTrace(fmt.Sprintf("nd-rating-sync: found %d stars for foobar2000 in %q", stars, path))
		}
		if stars, ok := ratingMusicBeeToStars(vs[0]); ok {
			found["MusicBee"] = stars
			logTrace(fmt.Sprintf("nd-rating-sync: found %d stars for MusicBee in %q", stars, path))
			// Overrides FMPS_RATING above: RATING is what MediaMonkey writes.
			found["MediaMonkey"] = stars
			logTrace(fmt.Sprintf("nd-rating-sync: found %d stars for MediaMonkey in %q", stars, path))
		}
	}
	return pickRating(tagOrder, found, invalid)
}

// parseFLACRating parses a FLAC stream for a recognised star rating.
func parseFLACRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromFLAC(data, path, tagOrder)
	return stars, ok
}

// ratingFromFLAC is parseFLACRating with the parse error kept. Empty data
// (the extractor found no fLaC magic) is reported as untagged, like every
// other format; a corrupt metadata block is an error.
func ratingFromFLAC(data []byte, path string, tagOrder []string) (int, bool, error) {
	if len(data) == 0 {
		return 0, false, nil
	}
	cmts, err := parseFLACVorbisComments(data)
	if err != nil {
		return 0, false, err
	}
	return ratingFromVorbisComments(cmts, path, tagOrder)
}

// flacMetadataBlockHeaderSize is the fixed FLAC metadata block header length
// (1 byte type + 3 bytes length).
const flacMetadataBlockHeaderSize = 4

// extractFLACMetadata reads only the VORBIS_COMMENT block from a FLAC file.
// It walks the metadata block headers (4 bytes each), Seeks past non-comment
// block bodies (which can be many MiB for PICTURE blocks), and reads only
// the VORBIS_COMMENT body itself. The returned slice is a synthesised FLAC
// containing the "fLaC" magic + the comment block marked as "last" — exactly
// what parseFLACRating walks. When the metadata ends cleanly without a
// VORBIS_COMMENT, returns a minimal "fLaC + empty PADDING last-block" so the
// parser reports tagAbsent.
//
// An ID3v2 tag in front of the stream (written by some taggers, skipped by
// libFLAC) is skipped. Any other start than "fLaC" means the file is not a
// FLAC stream: an error, so the file is unreadable rather than untagged.
func extractFLACMetadata(f *os.File) ([]byte, error) {
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return nil, flacMagicErr(err)
	}
	if string(magic[:3]) == "ID3" {
		if err := skipLeadingID3v2(f); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(f, magic[:]); err != nil {
			return nil, flacMagicErr(err)
		}
	}
	if string(magic[:]) != "fLaC" {
		return nil, errors.New("not a FLAC stream (fLaC magic missing)")
	}

	for blocks := 0; ; blocks++ {
		if blocks >= maxContainerEntries {
			return nil, errTooManyEntries
		}
		var hdr [flacMetadataBlockHeaderSize]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				// The previous block was not flagged as the last one, so the
				// metadata is cut off: unreadable, never "no tag".
				return nil, errors.New("FLAC metadata ends before its last block")
			}
			return nil, err
		}
		isLast := hdr[0]&0x80 != 0
		blockType := hdr[0] & 0x7F
		blockLen := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])

		if blockType == flacBlockVorbisComment {
			if blockLen > maxMetadataReadBytes {
				return nil, fmt.Errorf("FLAC VORBIS_COMMENT block size %d exceeds cap %d", blockLen, maxMetadataReadBytes)
			}
			// Synthesise: fLaC + VORBIS_COMMENT marked as last block, with the
			// body read straight into the synth (one allocation).
			out := make([]byte, 4+flacMetadataBlockHeaderSize+blockLen)
			copy(out, "fLaC")
			out[4], out[5], out[6], out[7] = 0x80|byte(flacBlockVorbisComment), hdr[1], hdr[2], hdr[3]
			if _, err := io.ReadFull(f, out[8:]); err != nil {
				return nil, err
			}
			return out, nil
		}

		if isLast {
			return flacSynthEmpty(), nil
		}

		// Skip non-comment block body without reading it.
		if _, err := f.Seek(int64(blockLen), io.SeekCurrent); err != nil {
			return nil, err
		}
	}
}

func flacMagicErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("file ends before the fLaC magic")
	}
	return err
}

// flacSynthEmpty returns a minimal "no comments" FLAC: fLaC magic + a
// zero-length PADDING block flagged as the last metadata block. The existing
// parser walks it and reports tagAbsent.
func flacSynthEmpty() []byte {
	// 0x81 = last-block flag (0x80) | block type 1 (PADDING).
	return []byte{'f', 'L', 'a', 'C', 0x81, 0x00, 0x00, 0x00}
}
