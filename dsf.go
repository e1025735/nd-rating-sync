package main

import "encoding/binary"

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
