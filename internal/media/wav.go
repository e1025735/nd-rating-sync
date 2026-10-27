package media

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
func parseWAVRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromWAV(data, path, tagOrder)
	return stars, ok
}

// ratingFromWAV is parseWAVRating with the error of the embedded ID3v2 tag
// kept, so a corrupt tag is reported as unreadable rather than untagged.
func ratingFromWAV(data []byte, path string, tagOrder []string) (int, bool, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, false, nil
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
			return ratingFromID3v2(data[pos:end], path, tagOrder)
		}
		// RIFF chunks are padded to even byte boundaries.
		if chunkSize%2 != 0 {
			end++
		}
		pos = end
	}
	return 0, false, nil
}

// extractWAVMetadata walks the RIFF chunk list at the start of a WAV file,
// Seeks past audio (`data`/`fmt `/etc.) chunks without reading their bodies,
// and returns a synthesised RIFF/WAVE container holding only the `id3 ` /
// `ID3 ` chunk when present. The existing parseWAVRating walks this synth
// identically to a real file.
//
// Only a chunk list walked to the end of the file without an ID3 chunk is
// untagged (a valid empty RIFF/WAVE synth). A file without the RIFF/WAVE
// magic (including RF64), cut off inside its header, or with a chunk that
// claims to run past the end of the file (a truncated file, or a streamed
// WAV whose data size was never filled in) is an error: an ID3 chunk behind
// such a chunk could not be found, so the file is unreadable, never untagged.
func extractWAVMetadata(f *os.File) ([]byte, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errors.New("file ends inside the RIFF header")
		}
		return nil, err
	}
	if string(hdr[:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF/WAVE file")
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := info.Size()

	pos := int64(len(hdr))
	for {
		var chHdr [8]byte
		if _, err := io.ReadFull(f, chHdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				// Every chunk was walked to the end of the file; fewer than
				// 8 trailing bytes cannot hold another chunk either.
				return wavSynthEmpty(hdr[:]), nil
			}
			return nil, err
		}
		fourCC := strings.ToLower(string(chHdr[:4]))
		chunkSize := int64(binary.LittleEndian.Uint32(chHdr[4:8]))
		body := pos + int64(len(chHdr))
		if chunkSize > fileSize-body {
			return nil, fmt.Errorf("WAV chunk %q claims %d bytes but only %d follow it", string(chHdr[:4]), chunkSize, fileSize-body)
		}

		if fourCC == "id3 " {
			if chunkSize > maxMetadataReadBytes {
				return nil, fmt.Errorf("WAV id3 chunk size %d exceeds cap %d", chunkSize, maxMetadataReadBytes)
			}
			// Synthesise: RIFF + (size) + WAVE + id3 chunk header + body,
			// reading the body straight into the synth (one allocation).
			out := make([]byte, 12+8+int(chunkSize))
			copy(out[0:4], hdr[:4])    // "RIFF"
			copy(out[8:12], hdr[8:12]) // "WAVE"
			copy(out[12:20], chHdr[:]) // id3 chunk header (fourCC + size)
			binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
			if _, err := io.ReadFull(f, out[20:]); err != nil {
				return nil, err
			}
			return out, nil
		}

		// Skip the chunk body (padded to an even length on disk) without
		// reading it. A missing pad byte after the last chunk is harmless:
		// the next header read simply hits the end of the file.
		pos = body + chunkSize + chunkSize%2
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
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
