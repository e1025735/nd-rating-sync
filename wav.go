package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
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

// extractWAVMetadata walks the RIFF chunk list at the start of a WAV file,
// Seeks past audio (`data`/`fmt `/etc.) chunks without reading their bodies,
// and returns a synthesised RIFF/WAVE container holding only the `id3 ` /
// `ID3 ` chunk when present. The existing parseWAVRating walks this synth
// identically to a real file.
//
// When no ID3 chunk is found the result is a valid empty RIFF/WAVE (parser
// reports tagAbsent). When the magic is missing or the file is truncated,
// returns (nil, nil) so the dispatch path also reports tagAbsent — matching
// the prior behaviour for malformed WAVs.
func extractWAVMetadata(f *os.File) ([]byte, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil
		}
		return nil, err
	}
	if string(hdr[:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, nil
	}

	for {
		var chHdr [8]byte
		if _, err := io.ReadFull(f, chHdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return wavSynthEmpty(hdr[:]), nil
			}
			return nil, err
		}
		fourCC := strings.ToLower(string(chHdr[:4]))
		chunkSize := int64(binary.LittleEndian.Uint32(chHdr[4:8]))
		if chunkSize < 0 {
			return wavSynthEmpty(hdr[:]), nil
		}
		// RIFF body length is padded to an even boundary on disk.
		paddedSize := chunkSize
		if chunkSize%2 != 0 {
			paddedSize++
		}

		if fourCC == "id3 " {
			if chunkSize > maxMetadataReadBytes {
				return nil, fmt.Errorf("WAV id3 chunk size %d exceeds cap %d", chunkSize, maxMetadataReadBytes)
			}
			body := make([]byte, chunkSize)
			if _, err := io.ReadFull(f, body); err != nil {
				return nil, err
			}
			// Synthesise: RIFF + (size) + WAVE + id3 chunk header + body.
			out := make([]byte, 0, 12+8+int(chunkSize))
			out = append(out, hdr[:4]...)             // "RIFF"
			out = append(out, 0, 0, 0, 0)             // RIFF size (filled below)
			out = append(out, hdr[8:12]...)           // "WAVE"
			out = append(out, chHdr[:]...)            // id3 chunk header (fourCC + size)
			out = append(out, body...)
			binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
			return out, nil
		}

		// Skip non-id3 chunk body without reading it.
		if _, err := f.Seek(paddedSize, io.SeekCurrent); err != nil {
			// Likely an unseekable stream or truncation — return what we have.
			return wavSynthEmpty(hdr[:]), nil
		}
	}
}

// wavSynthEmpty returns the bare RIFF/WAVE preamble with the original
// 12-byte header but no chunks. parseWAVRating walks zero chunks and
// reports tagAbsent.
func wavSynthEmpty(hdr []byte) []byte {
	out := make([]byte, 12)
	copy(out, hdr[:12])
	binary.LittleEndian.PutUint32(out[4:8], 4) // size = "WAVE" only
	return out
}
