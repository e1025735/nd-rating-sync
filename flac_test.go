package main

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeFLAC builds a minimal FLAC byte stream consisting of just the "fLaC"
// magic and a single VORBIS_COMMENT block (marked as last). The block has no
// STREAMINFO — the parser doesn't require it, since it walks blocks generically
// and only inspects type 4. Each comment must be a "KEY=value" string.
func makeFLAC(t *testing.T, comments ...string) []byte {
	t.Helper()
	var body bytes.Buffer
	const vendor = "test"
	require.NoError(t, binary.Write(&body, binary.LittleEndian, uint32(len(vendor))))
	body.WriteString(vendor)
	require.NoError(t, binary.Write(&body, binary.LittleEndian, uint32(len(comments))))
	for _, c := range comments {
		require.NoError(t, binary.Write(&body, binary.LittleEndian, uint32(len(c))))
		body.WriteString(c)
	}

	var out bytes.Buffer
	out.WriteString("fLaC")
	blockLen := body.Len()
	out.WriteByte(0x80 | flacBlockVorbisComment) // last-block flag | type 4
	out.WriteByte(byte(blockLen >> 16))
	out.WriteByte(byte(blockLen >> 8))
	out.WriteByte(byte(blockLen))
	out.Write(body.Bytes())
	return out.Bytes()
}

// makeFLACWithPadding emits a non-Vorbis block before the VORBIS_COMMENT block
// to verify the parser walks past unrelated blocks.
func makeFLACWithPadding(t *testing.T, comments ...string) []byte {
	t.Helper()

	// PADDING block (type 1), 8 bytes of zeros, not last.
	var padded bytes.Buffer
	padded.WriteString("fLaC")
	padded.WriteByte(0x01) // not-last | type 1
	padded.WriteByte(0x00)
	padded.WriteByte(0x00)
	padded.WriteByte(0x08)
	padded.Write(make([]byte, 8))

	// Then the Vorbis block from makeFLAC, minus its "fLaC" prefix.
	tail := makeFLAC(t, comments...)
	padded.Write(tail[4:])
	return padded.Bytes()
}

// ─── magic / structure ────────────────────────────────────────────────────────

func TestParseFLACVorbisComments_BadMagic(t *testing.T) {
	_, err := parseFLACVorbisComments([]byte("not a flac file"))
	assert.Error(t, err)
}

func TestParseFLACVorbisComments_EmptySlice(t *testing.T) {
	_, err := parseFLACVorbisComments([]byte{})
	assert.Error(t, err)
}

func TestParseFLACVorbisComments_NoVorbisBlock(t *testing.T) {
	// "fLaC" + a single PADDING block marked as last.
	data := []byte("fLaC")
	data = append(data, 0x81, 0x00, 0x00, 0x04) // last | type 1, length 4
	data = append(data, 0, 0, 0, 0)

	cmts, err := parseFLACVorbisComments(data)
	require.NoError(t, err)
	assert.Empty(t, cmts)
}

func TestParseFLACVorbisComments_WalksPastUnrelatedBlocks(t *testing.T) {
	data := makeFLACWithPadding(t, "FMPS_RATING=0.6")
	cmts, err := parseFLACVorbisComments(data)
	require.NoError(t, err)
	assert.Equal(t, []string{"0.6"}, cmts["FMPS_RATING"])
}

func TestParseFLACVorbisComments_BlockExceedsFile(t *testing.T) {
	// Header claims 1 MB body, but the file is shorter.
	data := []byte("fLaC")
	data = append(data, 0x84, 0x10, 0x00, 0x00) // last | type 4, length 0x100000
	data = append(data, 0x00, 0x00, 0x00, 0x00)

	_, err := parseFLACVorbisComments(data)
	assert.Error(t, err)
}

// ─── comment parsing ──────────────────────────────────────────────────────────

func TestParseFLACVorbisComments_CaseInsensitiveKeys(t *testing.T) {
	data := makeFLAC(t, "fmps_rating=0.6", "Artist=Foo")
	cmts, err := parseFLACVorbisComments(data)
	require.NoError(t, err)
	assert.Equal(t, []string{"0.6"}, cmts["FMPS_RATING"])
	assert.Equal(t, []string{"Foo"}, cmts["ARTIST"])
}

func TestParseFLACVorbisComments_MalformedEntryIgnored(t *testing.T) {
	// Second comment has no '=' — should be skipped, others still parsed.
	data := makeFLAC(t, "FMPS_RATING=0.4", "no_equals_sign", "TITLE=Hello")
	cmts, err := parseFLACVorbisComments(data)
	require.NoError(t, err)
	assert.Equal(t, []string{"0.4"}, cmts["FMPS_RATING"])
	assert.Equal(t, []string{"Hello"}, cmts["TITLE"])
}

// ─── parseFLACRating: MediaMonkey via FMPS_RATING ────────────────────────────

func TestParseFLACRating_MediaMonkeyCanonicalValues(t *testing.T) {
	cases := []struct {
		fmps string
		want int
	}{
		{"0.2", 1}, {"0.4", 2}, {"0.6", 3}, {"0.8", 4}, {"1.0", 5},
	}
	for _, tc := range cases {
		data := makeFLAC(t, "FMPS_RATING="+tc.fmps)
		stars, ok := parseFLACRating(data, []string{"MediaMonkey"})
		assert.True(t, ok, "FMPS_RATING=%s", tc.fmps)
		assert.Equal(t, tc.want, stars, "FMPS_RATING=%s", tc.fmps)
	}
}

func TestParseFLACRating_ZeroIsUnrated(t *testing.T) {
	data := makeFLAC(t, "FMPS_RATING=0.0")
	_, ok := parseFLACRating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseFLACRating_NoRecognisedTag(t *testing.T) {
	data := makeFLAC(t, "ARTIST=Foo", "TITLE=Bar")
	_, ok := parseFLACRating(data, []string{"MediaMonkey", "WMP", "iTunes"})
	assert.False(t, ok)
}

// ─── tag order interaction ────────────────────────────────────────────────────

func TestParseFLACRating_WMPAndITunesNeverMatchForFLAC(t *testing.T) {
	// FMPS_RATING is present, but tagOrder excludes MediaMonkey.
	// Since WMP/iTunes have no Vorbis representation, no rating wins.
	data := makeFLAC(t, "FMPS_RATING=0.8")
	_, ok := parseFLACRating(data, []string{"WMP", "iTunes"})
	assert.False(t, ok)
}

func TestParseFLACRating_TagOrderFallsThroughToMediaMonkey(t *testing.T) {
	data := makeFLAC(t, "FMPS_RATING=0.4")
	stars, ok := parseFLACRating(data, []string{"WMP", "iTunes", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

func TestParseFLACRating_EmptyTagOrderNeverMatches(t *testing.T) {
	data := makeFLAC(t, "FMPS_RATING=0.6")
	_, ok := parseFLACRating(data, []string{})
	assert.False(t, ok)
}

func TestParseFLACRating_BadFile(t *testing.T) {
	_, ok := parseFLACRating([]byte("definitely not flac"), []string{"MediaMonkey"})
	assert.False(t, ok)
}

// ─── parseFLACRating: foobar2000 via RATING ──────────────────────────────────

func TestParseFLACRating_foobar2000CanonicalValues(t *testing.T) {
	for n := 1; n <= 5; n++ {
		data := makeFLAC(t, "RATING="+strconv.Itoa(n))
		stars, ok := parseFLACRating(data, []string{"foobar2000"})
		assert.True(t, ok, "RATING=%d", n)
		assert.Equal(t, n, stars, "RATING=%d", n)
	}
}

func TestParseFLACRating_foobar2000UnratedValues(t *testing.T) {
	for _, v := range []string{"0", "", "6", "-1", "abc"} {
		data := makeFLAC(t, "RATING="+v)
		_, ok := parseFLACRating(data, []string{"foobar2000"})
		assert.False(t, ok, "RATING=%q should not produce a rating", v)
	}
}

func TestParseFLACRating_foobar2000VsMediaMonkeyOrder(t *testing.T) {
	// Both tags present: FMPS_RATING=0.4 (MM=2 stars), RATING=5 (foobar=5 stars).
	// tagOrder picks foobar2000 first.
	data := makeFLAC(t, "FMPS_RATING=0.4", "RATING=5")
	stars, ok := parseFLACRating(data, []string{"foobar2000", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)

	// Reverse the order — MediaMonkey wins.
	stars, ok = parseFLACRating(data, []string{"MediaMonkey", "foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

func TestParseFLACRating_MusicBeeCanonicalValues(t *testing.T) {
	cases := []struct {
		value string
		want  int
	}{
		{"20", 1}, {"40", 2}, {"60", 3}, {"80", 4}, {"100", 5},
	}
	for _, tc := range cases {
		data := makeFLAC(t, "RATING="+tc.value)
		stars, ok := parseFLACRating(data, []string{"MusicBee"})
		assert.True(t, ok, "RATING=%s", tc.value)
		assert.Equal(t, tc.want, stars, "RATING=%s", tc.value)
	}
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractFLACMetadata_SkipsPictureAndAudio proves the FLAC extractor
// Seeks past a giant PICTURE-like block AND the audio frames that follow —
// it only reads the VORBIS_COMMENT body. The sentinel sits inside both the
// PICTURE body and the trailing audio body; if the extractor reads through
// either it would appear in the returned synth.
func TestExtractFLACMetadata_SkipsPictureAndAudio(t *testing.T) {
	dir := t.TempDir()

	// makeFLAC builds "fLaC" + a single VORBIS_COMMENT (last). For this test
	// we splice a PICTURE-like block BEFORE the comment so the extractor
	// must Seek past it.
	cmt := makeFLAC(t, "FMPS_RATING=0.6") // 3 stars
	pictureBody := junkAudio()

	var withPicture bytes.Buffer
	withPicture.WriteString("fLaC")
	// PICTURE block (type 6), not last.
	withPicture.WriteByte(0x06)
	withPicture.WriteByte(byte(len(pictureBody) >> 16))
	withPicture.WriteByte(byte(len(pictureBody) >> 8))
	withPicture.WriteByte(byte(len(pictureBody)))
	withPicture.Write(pictureBody)
	// Append the VORBIS_COMMENT block from makeFLAC (drop its "fLaC" prefix).
	withPicture.Write(cmt[4:])
	// And junk "audio frames" after the metadata.
	withPicture.Write(junkAudio())

	path := writeBinFile(t, dir, "song.flac", withPicture.Bytes())

	data := extractedBytes(t, path, "flac")
	assert.NotContains(t, string(data), string(sentinel),
		"FLAC extractor must Seek past PICTURE block and audio frames")

	stars, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}
