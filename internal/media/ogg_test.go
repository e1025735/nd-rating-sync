//go:build !tinygo

package media

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

	stars, ok := parseOggVorbisRating(data, "", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

func TestParseOggVorbisRating_OpusFMPS(t *testing.T) {
	commentPkt := makeOpusCommentPacket(t, "FMPS_RATING=0.8")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, "", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

func TestParseOggVorbisRating_OpusFoobar2000(t *testing.T) {
	for n := 1; n <= 5; n++ {
		commentPkt := makeOpusCommentPacket(t, "RATING="+strconv.Itoa(n))
		data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
		stars, ok := parseOggVorbisRating(data, "", []string{"foobar2000"})
		assert.True(t, ok, "RATING=%d", n)
		assert.Equal(t, n, stars, "RATING=%d", n)
	}
}

func TestParseOggVorbisRating_VorbisFoobar2000(t *testing.T) {
	commentPkt := makeVorbisCommentPacket(t, "RATING=4")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, "", []string{"foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

func TestParseOggVorbisRating_LargeCommentPacketSpansPages(t *testing.T) {
	// Pad with a long comment so the packet exceeds one segment.
	long := bytes.Repeat([]byte("X"), 400)
	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.4", "PADDING="+string(long))
	data := makeOggMultiPage(t, 1, idHeaderPlaceholder, commentPkt)

	stars, ok := parseOggVorbisRating(data, "", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

func TestParseOggVorbisRating_NoMagic(t *testing.T) {
	// Comment packet starts with neither vorbis nor OpusTags magic.
	commentPkt := []byte("garbage payload not a real comment header at all")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	_, ok := parseOggVorbisRating(data, "", []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseOggVorbisRating_BadFile(t *testing.T) {
	_, ok := parseOggVorbisRating([]byte("not ogg at all"), "", []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseOggVorbisRating_NoCommentPacket(t *testing.T) {
	// Only one packet present.
	data := makeOggSinglePage(t, idHeaderPlaceholder)

	_, ok := parseOggVorbisRating(data, "", []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseOggVorbisRating_TagOrderFiltersWMPiTunes(t *testing.T) {
	// Even though FMPS_RATING is present, WMP/iTunes have no Vorbis mapping,
	// so MediaMonkey-less tagOrder produces no rating.
	commentPkt := makeOpusCommentPacket(t, "FMPS_RATING=1.0")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)

	_, ok := parseOggVorbisRating(data, "", []string{"WMP", "iTunes"})
	assert.False(t, ok)
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractOggMetadata_IgnoresTrailingAudio proves the extractor stops after
// the two header packets: a file padded with far more audio than metadata
// still yields a synth no bigger than the headers themselves.
func TestExtractOggMetadata_IgnoresTrailingAudio(t *testing.T) {
	dir := t.TempDir()

	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.6") // 3 stars
	good := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
	huge := append(good, junkAudio()...)
	path := writeBinFile(t, dir, "song.ogg", huge)

	data := extractedBytes(t, path, "ogg")
	assert.LessOrEqual(t, len(data), len(good), "Ogg extractor must not read past the header packets")
	assert.False(t, bytesContains(data, sentinel))

	stars, result := extractStarsFromFile(path, "ogg", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

// opusWithCoverArt builds an Opus file whose comment packet carries a 4 MiB
// METADATA_BLOCK_PICTURE (spanning dozens of pages) before the rating.
func opusWithCoverArt(t *testing.T) []byte {
	t.Helper()
	art := "METADATA_BLOCK_PICTURE=" + string(junkAudio())
	commentPkt := makeOpusCommentPacket(t, "TITLE=Song", art, "RATING=4")
	return makeOggMultiPage(t, 255, idHeaderPlaceholder, commentPkt)
}

// Cover art used to push the comment packet past the old fixed 512 KiB read,
// which made the rating look absent. The streaming extractor Seeks past the
// art without reading it and still finds the rating behind it.
func TestExtractOggMetadata_SkipsLargeCoverArt(t *testing.T) {
	dir := t.TempDir()
	path := writeBinFile(t, dir, "song.opus", opusWithCoverArt(t))

	data := extractedBytes(t, path, "opus")
	if bytesContains(data, sentinel) {
		t.Fatalf("Ogg extractor must Seek past cover art (sentinel found in %d-byte output)", len(data))
	}
	assert.Less(t, len(data), 4096, "synth must hold only the small header entries")

	stars, result := extractStarsFromFile(path, "opus", []string{"foobar2000"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}

// A file cut off inside the comment packet must be unreadable, never
// tagAbsent, so clear_rating_if_untagged cannot wipe its rating.
func TestExtractOggMetadata_TruncatedCommentIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	full := opusWithCoverArt(t)
	path := writeBinFile(t, dir, "song.opus", full[:len(full)/2])

	_, result := extractStarsFromFile(path, "opus", []string{"foobar2000"})
	assert.Equal(t, fileUnreadable, result)
}

func TestExtractOggMetadata_HugeCommentLengthIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	var pkt bytes.Buffer
	pkt.WriteString("OpusTags")
	require.NoError(t, binary.Write(&pkt, binary.LittleEndian, uint32(0)))          // vendor
	require.NoError(t, binary.Write(&pkt, binary.LittleEndian, uint32(1)))          // count
	require.NoError(t, binary.Write(&pkt, binary.LittleEndian, uint32(0xFFFFFFFF))) // entry length
	pkt.WriteString("RATING=4")
	path := writeBinFile(t, dir, "song.opus", makeOggSinglePage(t, idHeaderPlaceholder, pkt.Bytes()))

	_, result := extractStarsFromFile(path, "opus", []string{"foobar2000"})
	assert.Equal(t, fileUnreadable, result)
}

func TestExtractOggMetadata_SmallPictureCommentDropped(t *testing.T) {
	dir := t.TempDir()
	commentPkt := makeVorbisCommentPacket(t, "METADATA_BLOCK_PICTURE=QUJD", "RATING=60")
	path := writeBinFile(t, dir, "song.ogg", makeOggSinglePage(t, idHeaderPlaceholder, commentPkt))

	data := extractedBytes(t, path, "ogg")
	assert.False(t, bytesContains(data, []byte("METADATA_BLOCK_PICTURE")))

	stars, result := extractStarsFromFile(path, "ogg", []string{"MusicBee"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

// A file that is not an Ogg stream despite its extension cannot be read, so
// its rating must never be cleared.
func TestExtractOggMetadata_NotOggIsUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := writeBinFile(t, dir, "song.ogg", []byte("definitely not an ogg file"))

	_, result := extractStarsFromFile(path, "ogg", []string{"foobar2000"})
	assert.Equal(t, fileUnreadable, result)
}

// oggSynth must split packets with more than 255 lacing values over several
// pages so extractOggPackets reassembles them.
func TestOggSynth_RoundTripsLargePacket(t *testing.T) {
	big := bytes.Repeat([]byte{'x'}, 255*300+7)
	packets, err := extractOggPackets(oggSynth(idHeaderPlaceholder, big), 2)
	require.NoError(t, err)
	require.Len(t, packets, 2)
	assert.Equal(t, idHeaderPlaceholder, packets[0])
	assert.Equal(t, big, packets[1])
}

// Comments small enough to be kept must not add up past the per-file cap.
func TestExtractOggMetadata_KeptCommentsAreCapped(t *testing.T) {
	entry := "X=" + string(bytes.Repeat([]byte{'a'}, oggMaxKeptComment-2))
	count := maxMetadataReadBytes/len(entry) + 2
	var pkt bytes.Buffer
	pkt.WriteString("OpusTags")
	require.NoError(t, binary.Write(&pkt, binary.LittleEndian, uint32(0)))     // vendor
	require.NoError(t, binary.Write(&pkt, binary.LittleEndian, uint32(count))) // comment count
	for i := 0; i < count; i++ {
		require.NoError(t, binary.Write(&pkt, binary.LittleEndian, uint32(len(entry))))
		pkt.WriteString(entry)
	}
	path := writeBinFile(t, t.TempDir(), "song.opus", makeOggMultiPage(t, 255, idHeaderPlaceholder, pkt.Bytes()))

	_, result := extractStarsFromFile(path, "opus", []string{"foobar2000"})
	assert.Equal(t, fileUnreadable, result)
}
