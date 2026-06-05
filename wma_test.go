package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeUTF16LE encodes s as little-endian UTF-16 with a null terminator.
func encodeUTF16LE(s string) []byte {
	var out []byte
	for _, r := range s {
		u := uint16(r)
		out = append(out, byte(u), byte(u>>8))
	}
	return append(out, 0, 0) // null terminator
}

type wmaDescriptor struct {
	name      string
	valueType uint16 // 0=Unicode string, 5=WORD
	value     []byte
}

// buildWMA constructs a minimal ASF file with a single Extended Content
// Description Object containing the provided descriptors.
func buildWMA(t *testing.T, descriptors []wmaDescriptor) []byte {
	t.Helper()

	var extBody bytes.Buffer
	require.NoError(t, binary.Write(&extBody, binary.LittleEndian, uint16(len(descriptors))))
	for _, d := range descriptors {
		nameBytes := encodeUTF16LE(d.name)
		require.NoError(t, binary.Write(&extBody, binary.LittleEndian, uint16(len(nameBytes))))
		extBody.Write(nameBytes)
		require.NoError(t, binary.Write(&extBody, binary.LittleEndian, d.valueType))
		require.NoError(t, binary.Write(&extBody, binary.LittleEndian, uint16(len(d.value))))
		extBody.Write(d.value)
	}

	extObjSize := uint64(24 + extBody.Len()) // GUID(16) + size(8) + body

	var out bytes.Buffer
	out.Write(asfHeaderObjectGUID)
	headerSize := uint64(30 + 24 + extBody.Len())
	require.NoError(t, binary.Write(&out, binary.LittleEndian, headerSize))
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint32(1))) // 1 child object
	out.Write([]byte{0x01, 0x02})                                          // reserved
	out.Write(asfExtContentDescObjectGUID)
	require.NoError(t, binary.Write(&out, binary.LittleEndian, extObjSize))
	out.Write(extBody.Bytes())
	return out.Bytes()
}

func wmaWordValue(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

// ─── invalid / edge cases ─────────────────────────────────────────────────────

func TestParseWMARating_EmptySlice(t *testing.T) {
	_, ok := parseWMARating([]byte{}, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWMARating_TooShort(t *testing.T) {
	_, ok := parseWMARating(asfHeaderObjectGUID[:10], []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWMARating_InvalidMagic(t *testing.T) {
	data := make([]byte, 30)
	copy(data, "JUNK")
	_, ok := parseWMARating(data, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWMARating_NoExtContentDescObject(t *testing.T) {
	// Valid ASF header with zero child objects.
	var out bytes.Buffer
	out.Write(asfHeaderObjectGUID)
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint64(30)))
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint32(0)))
	out.Write([]byte{0x01, 0x02})
	_, ok := parseWMARating(out.Bytes(), []string{"WMP"})
	assert.False(t, ok)
}

// ─── WMP (WM/SharedUserRating) ────────────────────────────────────────────────

func TestParseWMARating_WMPCanonicalValues(t *testing.T) {
	cases := []struct {
		rating uint16
		want   int
	}{
		{1, 1}, {25, 2}, {50, 3}, {75, 4}, {99, 5},
	}
	for _, tc := range cases {
		data := buildWMA(t, []wmaDescriptor{
			{name: "WM/SharedUserRating", valueType: 5, value: wmaWordValue(tc.rating)},
		})
		stars, ok := parseWMARating(data, []string{"WMP"})
		assert.True(t, ok, "WM/SharedUserRating=%d", tc.rating)
		assert.Equal(t, tc.want, stars, "WM/SharedUserRating=%d", tc.rating)
	}
}

func TestParseWMARating_WMPZeroIsUnrated(t *testing.T) {
	data := buildWMA(t, []wmaDescriptor{
		{name: "WM/SharedUserRating", valueType: 5, value: wmaWordValue(0)},
	})
	_, ok := parseWMARating(data, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWMARating_WMPWrongValueTypeIgnored(t *testing.T) {
	// FMPS_Rating as a string masquerading as WM/SharedUserRating with wrong type.
	data := buildWMA(t, []wmaDescriptor{
		{name: "WM/SharedUserRating", valueType: 0, value: encodeUTF16LE("99")},
	})
	_, ok := parseWMARating(data, []string{"WMP"})
	assert.False(t, ok)
}

// ─── MediaMonkey (FMPS_Rating) ────────────────────────────────────────────────

func TestParseWMARating_MediaMonkeyCanonicalValues(t *testing.T) {
	cases := []struct {
		fmps string
		want int
	}{
		{"0.2", 1}, {"0.4", 2}, {"0.6", 3}, {"0.8", 4}, {"1.0", 5},
	}
	for _, tc := range cases {
		data := buildWMA(t, []wmaDescriptor{
			{name: "FMPS_Rating", valueType: 0, value: encodeUTF16LE(tc.fmps)},
		})
		stars, ok := parseWMARating(data, []string{"MediaMonkey"})
		assert.True(t, ok, "FMPS_Rating=%s", tc.fmps)
		assert.Equal(t, tc.want, stars, "FMPS_Rating=%s", tc.fmps)
	}
}

func TestParseWMARating_MediaMonkeyZeroIsUnrated(t *testing.T) {
	data := buildWMA(t, []wmaDescriptor{
		{name: "FMPS_Rating", valueType: 0, value: encodeUTF16LE("0.0")},
	})
	_, ok := parseWMARating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

// ─── tag order / priority ─────────────────────────────────────────────────────

func TestParseWMARating_TagOrderWMPBeatsMediaMonkey(t *testing.T) {
	data := buildWMA(t, []wmaDescriptor{
		{name: "WM/SharedUserRating", valueType: 5, value: wmaWordValue(99)},
		{name: "FMPS_Rating", valueType: 0, value: encodeUTF16LE("0.2")},
	})

	stars, ok := parseWMARating(data, []string{"WMP", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)

	stars, ok = parseWMARating(data, []string{"MediaMonkey", "WMP"})
	assert.True(t, ok)
	assert.Equal(t, 1, stars)
}

func TestParseWMARating_EmptyTagOrderNeverMatches(t *testing.T) {
	data := buildWMA(t, []wmaDescriptor{
		{name: "WM/SharedUserRating", valueType: 5, value: wmaWordValue(75)},
	})
	_, ok := parseWMARating(data, []string{})
	assert.False(t, ok)
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractWMAMetadata_SkipsDataObject proves the WMA extractor Seeks past
// non-ECDO ASF objects — especially the ASF Data Object that holds the
// audio payload. The sentinel lives inside a synthetic non-ECDO object
// placed BEFORE the ECDO; the extractor must seek past it instead of
// reading it.
func TestExtractWMAMetadata_SkipsDataObject(t *testing.T) {
	dir := t.TempDir()

	// Build a real ECDO via buildWMA, then take just the ECDO portion
	// (skipping the outer 30-byte ASF header preamble) so we can splice it
	// behind a fake junk object.
	realWMA := buildWMA(t, []wmaDescriptor{{
		name: "FMPS_Rating", valueType: 0, value: encodeUTF16LE("0.4"), // 2 stars
	}})
	ecdoObj := realWMA[30:] // ECDO header + body

	// Junk object: a non-ECDO GUID + giant body (sentinel + zeros).
	junkGUID := bytes.Repeat([]byte{0xAA}, 16)
	junkBody := junkAudio()
	junkObj := make([]byte, 0, 24+len(junkBody))
	junkObj = append(junkObj, junkGUID...)
	szBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(szBuf, uint64(24+len(junkBody)))
	junkObj = append(junkObj, szBuf...)
	junkObj = append(junkObj, junkBody...)

	// Top-level ASF Header Object with 2 child objects: junk first, then ECDO.
	totalSize := uint64(30 + len(junkObj) + len(ecdoObj))
	var w bytes.Buffer
	w.Write(asfHeaderObjectGUID)
	totalBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(totalBuf, totalSize)
	w.Write(totalBuf)
	numBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(numBuf, 2)
	w.Write(numBuf)
	w.Write([]byte{0, 0}) // reserved
	w.Write(junkObj)
	w.Write(ecdoObj)

	path := writeBinFile(t, dir, "song.wma", w.Bytes())

	data := extractedBytes(t, path, "wma")
	assert.NotContains(t, string(data), string(sentinel),
		"WMA extractor must Seek past non-ECDO objects")

	stars, result := extractStarsFromFile(path, "wma", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 2, stars)
}
