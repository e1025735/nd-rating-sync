package main

import (
	"errors"
	"io"
	"os"
)

// oggMetadataReadHint is the upper bound on bytes the Ogg extractor reads
// from the start of the file. The Vorbis/Opus comment packet always sits
// in the second logical packet, which by spec finishes within the first few
// pages (each ≤ 65307 bytes). 512 KiB safely covers comment packets that
// span pages because of large METADATA_BLOCK_PICTURE entries; anything
// beyond that is pathological for a comment header.
const oggMetadataReadHint = 512 * 1024

// extractOggPackets walks an Ogg bitstream, defragments segments into packets,
// and returns up to maxPackets packets from the leading logical bitstream.
// Continued packets that span pages (or segments at the 255-byte boundary)
// are reassembled. Subsequent chained streams are not read.
//
// Per the Ogg spec (RFC 3533), each page header is 27 fixed bytes plus a
// segment table; a packet ends on the first segment with length < 255.
// The 32-bit page checksum is not verified — we only need the comment
// header, never the audio data, so a permissive walk is fine.
func extractOggPackets(data []byte, maxPackets int) ([][]byte, error) {
	var packets [][]byte
	var current []byte
	pos := 0
	for pos < len(data) && len(packets) < maxPackets {
		if pos+27 > len(data) || string(data[pos:pos+4]) != "OggS" {
			return nil, errors.New("not an Ogg stream (OggS magic missing)")
		}
		nSegs := int(data[pos+26])
		headerEnd := pos + 27 + nSegs
		if headerEnd > len(data) {
			return nil, errors.New("truncated Ogg segment table")
		}
		segTable := data[pos+27 : headerEnd]

		offset := headerEnd
		for _, segLen := range segTable {
			end := offset + int(segLen)
			if end > len(data) {
				return nil, errors.New("Ogg segment exceeds file")
			}
			current = append(current, data[offset:end]...)
			offset = end
			if segLen < 255 {
				packets = append(packets, current)
				current = nil
				if len(packets) >= maxPackets {
					break
				}
			}
		}
		pos = offset
	}
	return packets, nil
}

// parseOggVorbisRating parses an Ogg-Vorbis or Ogg-Opus stream for a star
// rating. The Ogg container's second packet is the comment header; after
// stripping the format-specific magic prefix, its body is a standard Vorbis
// comment block (vendor + count + entries).
//
// For Vorbis the comment packet starts with byte 0x03 then "vorbis"; for
// Opus it starts with "OpusTags". A trailing Vorbis "framing bit" (Vorbis
// only) is ignored — parseVorbisCommentBlock stops once it has read the
// declared comment count.
func parseOggVorbisRating(data []byte, tagOrder []string) (int, bool) {
	packets, err := extractOggPackets(data, 2)
	if err != nil || len(packets) < 2 {
		return 0, false
	}
	commentPkt := packets[1]

	var body []byte
	switch {
	case len(commentPkt) >= 7 && commentPkt[0] == 0x03 && string(commentPkt[1:7]) == "vorbis":
		body = commentPkt[7:]
	case len(commentPkt) >= 8 && string(commentPkt[:8]) == "OpusTags":
		body = commentPkt[8:]
	default:
		return 0, false
	}

	cmts, err := parseVorbisCommentBlock(body)
	if err != nil {
		return 0, false
	}
	return ratingFromVorbisComments(cmts, tagOrder)
}

// extractOggMetadata reads just enough of the Ogg bitstream from the start
// of the file for the parser to recover the comment packet. The comment
// packet lives in the second logical packet of the leading stream, which
// the spec puts within the first few pages — so we cap the read at
// oggMetadataReadHint regardless of total file length. For audio files
// up to multi-GiB this is many orders of magnitude less I/O than reading
// the whole file.
func extractOggMetadata(f *os.File) ([]byte, error) {
	buf := make([]byte, oggMetadataReadHint)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	return buf[:n], nil
}
