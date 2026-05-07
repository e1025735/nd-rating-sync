package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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
	r := bytes.NewReader(data[4:])

	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return nil, fmt.Errorf("metadata block header: %w", err)
		}
		isLast := hdr[0]&0x80 != 0
		blockType := hdr[0] & 0x7F
		blockLen := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])

		if blockLen > r.Len() {
			return nil, errors.New("metadata block extends past end of file")
		}
		body := make([]byte, blockLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("metadata block body: %w", err)
		}

		if blockType == flacBlockVorbisComment {
			return parseVorbisCommentBlock(body)
		}
		if isLast {
			return vorbisComments{}, nil
		}
	}
}

// parseVorbisCommentBlock decodes the body of a VORBIS_COMMENT metadata block.
// Layout: vendor_length (u32 LE) + vendor + count (u32 LE) + count×(len + "KEY=value").
func parseVorbisCommentBlock(body []byte) (vorbisComments, error) {
	out := vorbisComments{}
	r := bytes.NewReader(body)

	readString := func() ([]byte, error) {
		var n uint32
		if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
			return nil, err
		}
		if int(n) > r.Len() {
			return nil, fmt.Errorf("string length %d exceeds remaining %d", n, r.Len())
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return buf, nil
	}

	if _, err := readString(); err != nil {
		return nil, fmt.Errorf("vendor string: %w", err)
	}

	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("comment count: %w", err)
	}
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
		out[key] = append(out[key], string(buf[eq+1:]))
	}
	return out, nil
}

// ratingFromVorbisComments resolves a star rating from a Vorbis comment map
// using tagOrder priority. Shared by the FLAC and Ogg/Opus paths. Recognised
// source applications:
//
//   - "MediaMonkey" – FMPS_RATING (float 0.0–1.0)
//   - "foobar2000"  – RATING (integer 1–5)
//
// "WMP" and "iTunes" have no canonical Vorbis representation and are silently
// skipped — listing them in tagOrder is harmless, they just never match.
func ratingFromVorbisComments(cmts vorbisComments, tagOrder []string) (int, bool) {
	found := map[string]int{}
	if vs := cmts["FMPS_RATING"]; len(vs) > 0 {
		if stars, ok := fmpsToStars(vs[0]); ok {
			found["MediaMonkey"] = stars
		}
	}
	if vs := cmts["RATING"]; len(vs) > 0 {
		if stars, ok := ratingIntToStars(vs[0]); ok {
			found["foobar2000"] = stars
		}
	}
	for _, format := range tagOrder {
		if stars, ok := found[format]; ok {
			return stars, true
		}
	}
	return 0, false
}

// parseFLACRating parses a FLAC stream for a recognised star rating.
func parseFLACRating(data []byte, tagOrder []string) (int, bool) {
	cmts, err := parseFLACVorbisComments(data)
	if err != nil {
		return 0, false
	}
	return ratingFromVorbisComments(cmts, tagOrder)
}
