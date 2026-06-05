package main

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Ogg test helpers ─────────────────────────────────────────────────────────

// makeOggPage builds a single Ogg page from a slice of segment bodies. Each
// segment must be ≤255 bytes. The page checksum is left as 0 — our parser
// does not verify it.
func makeOggPage(t *testing.T, isFirst bool, segments [][]byte) []byte {
	t.Helper()
	if len(segments) > 255 {
		t.Fatalf("page can hold at most 255 segments, got %d", len(segments))
	}
	var buf bytes.Buffer
	buf.WriteString("OggS")
	buf.WriteByte(0) // stream_structure_version
	var flags byte
	if isFirst {
		flags |= 0x02 // BOS
	}
	buf.WriteByte(flags)
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint64(0))) // granule
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(0))) // serial
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(0))) // page seq
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(0))) // checksum
	buf.WriteByte(byte(len(segments)))
	for _, s := range segments {
		if len(s) > 255 {
			t.Fatalf("segment exceeds 255 bytes: %d", len(s))
		}
		buf.WriteByte(byte(len(s)))
	}
	for _, s := range segments {
		buf.Write(s)
	}
	return buf.Bytes()
}

// packetToSegments splits a packet into Ogg lacing segments. Per the spec,
// a packet is encoded as N×255-byte segments followed by one terminating
// segment of length < 255 (which may be zero if the packet length is an
// exact multiple of 255).
func packetToSegments(pkt []byte) [][]byte {
	var segs [][]byte
	for len(pkt) >= 255 {
		segs = append(segs, append([]byte(nil), pkt[:255]...))
		pkt = pkt[255:]
	}
	segs = append(segs, append([]byte(nil), pkt...))
	return segs
}

// makeOggSinglePage emits all packets onto one Ogg page (capacity ≤255 segs).
func makeOggSinglePage(t *testing.T, packets ...[]byte) []byte {
	t.Helper()
	var allSegs [][]byte
	for _, p := range packets {
		allSegs = append(allSegs, packetToSegments(p)...)
	}
	return makeOggPage(t, true, allSegs)
}

// makeOggMultiPage splits segments evenly across pages (segsPerPage each).
// Useful for verifying the parser reassembles continued packets across pages.
func makeOggMultiPage(t *testing.T, segsPerPage int, packets ...[]byte) []byte {
	t.Helper()
	var allSegs [][]byte
	for _, p := range packets {
		allSegs = append(allSegs, packetToSegments(p)...)
	}
	var out bytes.Buffer
	for i := 0; i < len(allSegs); i += segsPerPage {
		end := i + segsPerPage
		if end > len(allSegs) {
			end = len(allSegs)
		}
		out.Write(makeOggPage(t, i == 0, allSegs[i:end]))
	}
	return out.Bytes()
}

// makeVorbisCommentPacket builds the body of an Ogg-Vorbis comment header.
// Layout: 0x03 + "vorbis" + Vorbis comment block + framing bit (0x01).
func makeVorbisCommentPacket(t *testing.T, comments ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteByte(0x03)
	buf.WriteString("vorbis")
	writeVorbisCommentBlock(t, &buf, comments)
	buf.WriteByte(0x01) // framing bit
	return buf.Bytes()
}

// makeOpusCommentPacket builds the body of an Ogg-Opus comment header.
// Layout: "OpusTags" + Vorbis comment block (no framing bit).
func makeOpusCommentPacket(t *testing.T, comments ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("OpusTags")
	writeVorbisCommentBlock(t, &buf, comments)
	return buf.Bytes()
}

func writeVorbisCommentBlock(t *testing.T, buf *bytes.Buffer, comments []string) {
	t.Helper()
	const vendor = "test"
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(len(vendor))))
	buf.WriteString(vendor)
	require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(len(comments))))
	for _, c := range comments {
		require.NoError(t, binary.Write(buf, binary.LittleEndian, uint32(len(c))))
		buf.WriteString(c)
	}
}

// idHeaderPlaceholder stands in for the first packet of a real bitstream.
// parseOggVorbisRating only inspects packet 1 (the comment header), so the
// content of packet 0 is irrelevant.
var idHeaderPlaceholder = []byte("ID-PACKET-0")

// ─── extractOggPackets ────────────────────────────────────────────────────────

func TestExtractOggPackets_BadMagic(t *testing.T) {
	_, err := extractOggPackets([]byte("not an ogg"), 2)
	assert.Error(t, err)
}

func TestExtractOggPackets_TwoSmallPackets(t *testing.T) {
	p0 := []byte("first")
	p1 := []byte("second")
	data := makeOggSinglePage(t, p0, p1)

	pkts, err := extractOggPackets(data, 2)
	require.NoError(t, err)
	require.Len(t, pkts, 2)
	assert.Equal(t, p0, pkts[0])
	assert.Equal(t, p1, pkts[1])
}

func TestExtractOggPackets_LargePacketSpansSegments(t *testing.T) {
	// 600-byte packet → segments [255][255][90].
	p0 := bytes.Repeat([]byte{0xAB}, 600)
	p1 := []byte("tail")
	data := makeOggSinglePage(t, p0, p1)

	pkts, err := extractOggPackets(data, 2)
	require.NoError(t, err)
	require.Len(t, pkts, 2)
	assert.Equal(t, p0, pkts[0])
	assert.Equal(t, p1, pkts[1])
}

func TestExtractOggPackets_ExactMultipleOf255(t *testing.T) {
	// Packet length is exactly 255 → must be encoded as [255][0]
	// (otherwise the parser can't tell where the packet ends).
	p0 := bytes.Repeat([]byte{0xCD}, 255)
	p1 := []byte("after")
	data := makeOggSinglePage(t, p0, p1)

	pkts, err := extractOggPackets(data, 2)
	require.NoError(t, err)
	require.Len(t, pkts, 2)
	assert.Equal(t, p0, pkts[0])
	assert.Equal(t, p1, pkts[1])
}

func TestExtractOggPackets_PacketSpansPages(t *testing.T) {
	// 600-byte packet split across pages with 1 segment per page → 3 pages
	// for the first packet, then more pages for the second.
	p0 := bytes.Repeat([]byte{0xEF}, 600)
	p1 := []byte("tail")
	data := makeOggMultiPage(t, 1, p0, p1)

	pkts, err := extractOggPackets(data, 2)
	require.NoError(t, err)
	require.Len(t, pkts, 2)
	assert.Equal(t, p0, pkts[0])
	assert.Equal(t, p1, pkts[1])
}

// ─── parseOggVorbisRating ─────────────────────────────────────────────────────

func TestParseOggVorbisRating_VorbisFMPS(t *testing.T) {
	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.6")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

func TestParseOggVorbisRating_OpusFMPS(t *testing.T) {
	commentPkt := makeOpusCommentPacket(t, "FMPS_RATING=0.8")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

func TestParseOggVorbisRating_OpusFoobar2000(t *testing.T) {
	for n := 1; n <= 5; n++ {
		commentPkt := makeOpusCommentPacket(t, "RATING="+strconv.Itoa(n))
		data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
		stars, ok := parseOggVorbisRating(data, []string{"foobar2000"})
		assert.True(t, ok, "RATING=%d", n)
		assert.Equal(t, n, stars, "RATING=%d", n)
	}
}

func TestParseOggVorbisRating_VorbisFoobar2000(t *testing.T) {
	commentPkt := makeVorbisCommentPacket(t, "RATING=4")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, []string{"foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

func TestParseOggVorbisRating_LargeCommentPacketSpansPages(t *testing.T) {
	// Pad with a long comment so the packet exceeds one segment.
	long := bytes.Repeat([]byte("X"), 400)
	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.4", "PADDING="+string(long))
	data := makeOggMultiPage(t, 1, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

func TestParseOggVorbisRating_NoMagic(t *testing.T) {
	// Comment packet starts with neither vorbis nor OpusTags magic.
	commentPkt := []byte("garbage payload not a real comment header at all")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	_, ok := parseOggVorbisRating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseOggVorbisRating_BadFile(t *testing.T) {
	_, ok := parseOggVorbisRating([]byte("not ogg at all"), []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseOggVorbisRating_NoCommentPacket(t *testing.T) {
	// Only one packet present.
	data := makeOggSinglePage(t, idHeaderPlaceholder)

	_, ok := parseOggVorbisRating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseOggVorbisRating_TagOrderFiltersWMPiTunes(t *testing.T) {
	// Even though FMPS_RATING is present, WMP/iTunes have no Vorbis mapping,
	// so MediaMonkey-less tagOrder produces no rating.
	commentPkt := makeOpusCommentPacket(t, "FMPS_RATING=1.0")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	_, ok := parseOggVorbisRating(data, []string{"WMP", "iTunes"})
	assert.False(t, ok)
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractOggMetadata_CapsReadAtHint proves the Ogg extractor never reads
// more than oggMetadataReadHint bytes from the file regardless of how big
// the file is on disk. The Vorbis/Opus spec guarantees the comment packet
// finishes within the first few pages, well inside that hint — so the cap
// is what makes Ogg/Opus reads bounded for multi-GB audio files.
func TestExtractOggMetadata_CapsReadAtHint(t *testing.T) {
	dir := t.TempDir()

	// Build a real Ogg-Vorbis file the parser can rate, then pad it with
	// way more bytes than the hint. The extractor should still return at
	// most oggMetadataReadHint bytes.
	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.6") // 3 stars
	good := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
	huge := append(good, make([]byte, oggMetadataReadHint*4)...) // 4× the hint past the metadata
	path := writeBinFile(t, dir, "song.ogg", huge)

	data := extractedBytes(t, path, "ogg")
	assert.LessOrEqual(t, len(data), oggMetadataReadHint,
		"Ogg extractor must cap its read at oggMetadataReadHint")

	stars, result := extractStarsFromFile(path, "ogg", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}
