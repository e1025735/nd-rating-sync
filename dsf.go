package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// parseDSFRating reads the ID3v2 block from a DSD Stream File. The DSD chunk
// header (always 28 bytes) stores the ID3v2 offset at bytes 20–27; an offset
// of 0 means the file has no tag.
func parseDSFRating(data []byte, tagOrder []string) (int, bool) {
	if len(data) < 28 || string(data[:4]) != "DSD " {
		return 0, false
	}
	id3Offset := binary.LittleEndian.Uint64(data[20:28])
	if id3Offset == 0 || id3Offset >= uint64(len(data)) {
		return 0, false
	}
	return parseID3v2Rating(data[id3Offset:], tagOrder)
}

// dsfHeaderSize is the fixed length of the DSD chunk header at the start of
// every DSF file.
const dsfHeaderSize = 28

// extractDSFMetadata reads the 28-byte DSD header, follows the embedded ID3
// offset (which can sit many GiB into the file, well past all the DSD audio
// samples), reads only the ID3 tag, and returns a synthesised DSF whose
// header points at the tag immediately following — exactly what
// parseDSFRating walks. Audio samples between header and tag are never
// touched.
func extractDSFMetadata(f *os.File) ([]byte, error) {
	var hdr [dsfHeaderSize]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		return nil, err
	}
	if string(hdr[:4]) != "DSD " {
		return nil, nil
	}
	id3Offset := binary.LittleEndian.Uint64(hdr[20:28])
	if id3Offset == 0 {
		// No tag. Return header as-is; parser sees offset=0 → tagAbsent.
		out := make([]byte, dsfHeaderSize)
		copy(out, hdr[:])
		return out, nil
	}

	tag, err := readID3v2TagAt(f, int64(id3Offset))
	if err != nil {
		return nil, err
	}
	if len(tag) == 0 {
		// Offset present but no parseable tag there. Pretend untagged.
		out := make([]byte, dsfHeaderSize)
		copy(out, hdr[:])
		binary.LittleEndian.PutUint64(out[20:28], 0)
		return out, nil
	}

	// Synthesise: original header but with id3Offset = dsfHeaderSize, then
	// the tag placed immediately after. parseDSFRating reads the offset,
	// jumps to byte 28, and finds the tag.
	out := make([]byte, dsfHeaderSize+len(tag))
	copy(out[:dsfHeaderSize], hdr[:])
	binary.LittleEndian.PutUint64(out[20:28], dsfHeaderSize)
	copy(out[dsfHeaderSize:], tag)
	return out, nil
}
