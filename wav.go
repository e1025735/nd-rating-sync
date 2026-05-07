package main

import (
	"encoding/binary"
	"strings"
)

// parseWAVRating finds the ID3v2 chunk inside a RIFF/WAVE container and
// delegates to parseID3v2Rating. Both "id3 " and "ID3 " fourCCs are accepted
// (different tag editors write different cases).
func parseWAVRating(data []byte, tagOrder []string) (int, bool) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, false
	}
	pos := 12
	for pos+8 <= len(data) {
		fourCC := strings.ToLower(string(data[pos : pos+4]))
		chunkSize := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		end := pos + chunkSize
		if end > len(data) {
			break
		}
		if fourCC == "id3 " {
			return parseID3v2Rating(data[pos:end], tagOrder)
		}
		// RIFF chunks are padded to even byte boundaries.
		if chunkSize%2 != 0 {
			end++
		}
		pos = end
	}
	return 0, false
}
