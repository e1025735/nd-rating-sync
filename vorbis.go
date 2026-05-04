package main

import (
	"bytes"
	"encoding/binary"
	"strings"
)

// ─── FLAC ─────────────────────────────────────────────────────────────────────

// parseFlacVorbisRating looks for a VORBIS_COMMENT metadata block (type 4) in
// raw FLAC data and extracts the FMPS_RATING field if present.
func parseFlacVorbisRating(data []byte) (int, bool) {
	// fLaC magic
	if !bytes.HasPrefix(data, []byte("fLaC")) {
		return 0, false
	}
	pos := 4

	for pos+4 <= len(data) {
		blockHeader := data[pos]
		blockType := blockHeader & 0x7F
		isLast := blockHeader&0x80 != 0
		blockLen := int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4

		if pos+blockLen > len(data) {
			break
		}

		if blockType == 4 { // VORBIS_COMMENT
			return parseVorbisComments(data[pos : pos+blockLen])
		}

		pos += blockLen
		if isLast {
			break
		}
	}
	return 0, false
}

// ─── Ogg Vorbis / Opus ────────────────────────────────────────────────────────

// parseOggVorbisRating finds the Vorbis comment header packet inside an Ogg
// bitstream and extracts the FMPS_RATING field.
//
// Ogg page layout (simplified):
//   - 4-byte capture pattern "OggS"
//   - 1-byte version (0)
//   - 1-byte header type
//   - 8-byte granule position
//   - 4-byte bitstream serial
//   - 4-byte page sequence number
//   - 4-byte CRC
//   - 1-byte segment count
//   - N segment size bytes
//   - data segments
//
// The comment header is the second logical packet in the stream.
func parseOggVorbisRating(data []byte) (int, bool) {
	// Collect logical packets until we find a Vorbis comment header.
	pos := 0
	packetIndex := 0

	for pos+27 <= len(data) {
		if !bytes.Equal(data[pos:pos+4], []byte("OggS")) {
			break
		}
		// header_type at data[pos+5]; granule(8)+serial(4)+seq(4)+crc(4) = 20 bytes
		segCount := int(data[pos+26])
		pos += 27

		if pos+segCount > len(data) {
			break
		}

		// Read segment sizes to get total page data length and packets.
		segSizes := data[pos : pos+segCount]
		pos += segCount

		pageDataLen := 0
		for _, sz := range segSizes {
			pageDataLen += int(sz)
		}
		if pos+pageDataLen > len(data) {
			break
		}
		pageData := data[pos : pos+pageDataLen]
		pos += pageDataLen

		// Split page data into packets (a segment < 255 ends a packet).
		pStart := 0
		for _, sz := range segSizes {
			pStart += int(sz)
			if sz < 255 {
				// End of packet.
				packet := pageData[:pStart]
				if isVorbisCommentPacket(packet) {
					return parseVorbisCommentPacket(packet)
				}
				packetIndex++
				if packetIndex > 3 {
					// Comment header is always within the first few packets.
					return 0, false
				}
				pageData = pageData[pStart:]
				pStart = 0
			}
		}
	}
	return 0, false
}

// isVorbisCommentPacket returns true if the packet is a Vorbis comment header
// (packet type 0x03 followed by "vorbis") or an Opus comment header
// ("OpusTags").
func isVorbisCommentPacket(p []byte) bool {
	if len(p) >= 7 && p[0] == 0x03 && bytes.Equal(p[1:7], []byte("vorbis")) {
		return true
	}
	if len(p) >= 8 && bytes.Equal(p[:8], []byte("OpusTags")) {
		return true
	}
	return false
}

// parseVorbisCommentPacket strips the packet header and delegates to the
// common Vorbis comment parser.
func parseVorbisCommentPacket(p []byte) (int, bool) {
	// Vorbis: skip 7-byte header ("0x03" + "vorbis")
	// Opus:   skip 8-byte header ("OpusTags")
	var payload []byte
	if len(p) >= 7 && p[0] == 0x03 {
		payload = p[7:]
	} else if len(p) >= 8 {
		payload = p[8:]
	} else {
		return 0, false
	}
	return parseVorbisComments(payload)
}

// ─── Common Vorbis comment parser ─────────────────────────────────────────────

// parseVorbisComments reads the Vorbis comment binary format:
//
//	4-byte LE vendor string length
//	<vendor string bytes>
//	4-byte LE comment count
//	for each comment:
//	  4-byte LE length
//	  <"KEY=VALUE" bytes>
//
// Returns the FMPS_RATING value converted to 1–5 stars, or (0, false).
func parseVorbisComments(data []byte) (int, bool) {
	if len(data) < 8 {
		return 0, false
	}

	// Skip vendor string.
	vendorLen := int(binary.LittleEndian.Uint32(data[:4]))
	pos := 4 + vendorLen
	if pos+4 > len(data) {
		return 0, false
	}

	commentCount := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
	pos += 4

	for i := 0; i < commentCount; i++ {
		if pos+4 > len(data) {
			break
		}
		commentLen := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		pos += 4

		if pos+commentLen > len(data) {
			break
		}
		comment := string(data[pos : pos+commentLen])
		pos += commentLen

		// KEY=VALUE – case-insensitive key comparison per Vorbis spec.
		eqIdx := strings.IndexByte(comment, '=')
		if eqIdx < 0 {
			continue
		}
		key := strings.ToUpper(comment[:eqIdx])
		val := comment[eqIdx+1:]

		if key == "FMPS_RATING" {
			return fmpsToStars(val)
		}
	}
	return 0, false
}
