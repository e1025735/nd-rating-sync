package main

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeDSF builds a minimal DSF file: a 28-byte DSD chunk header followed
// immediately by id3Data. If id3Data is nil the ID3v2 offset is set to 0
// (no tag).
func makeDSF(t *testing.T, id3Data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("DSD ")
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint64(28))) // DSD chunk size
	totalSize := uint64(28 + len(id3Data))
	require.NoError(t, binary.Write(&out, binary.LittleEndian, totalSize))
	var id3Off uint64
	if len(id3Data) > 0 {
		id3Off = 28
	}
	require.NoError(t, binary.Write(&out, binary.LittleEndian, id3Off))
	out.Write(id3Data)
	return out.Bytes()
}

// ─── invalid / edge cases ─────────────────────────────────────────────────────

func TestParseDSFRating_EmptySlice(t *testing.T) {
	_, ok := parseDSFRating([]byte{}, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseDSFRating_TooShort(t *testing.T) {
	_, ok := parseDSFRating([]byte("DSD "), []string{"WMP"})
	assert.False(t, ok)
}

func TestParseDSFRating_InvalidMagic(t *testing.T) {
	header := make([]byte, 28)
	copy(header, "FAKE")
	_, ok := parseDSFRating(header, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseDSFRating_ZeroOffset(t *testing.T) {
	data := makeDSF(t, nil)
	_, ok := parseDSFRating(data, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseDSFRating_OffsetPastEnd(t *testing.T) {
	var out bytes.Buffer
	out.WriteString("DSD ")
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint64(28)))
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint64(28)))
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint64(9999))) // offset past EOF
	_, ok := parseDSFRating(out.Bytes(), []string{"WMP"})
	assert.False(t, ok)
}

// ─── tag source coverage ──────────────────────────────────────────────────────

func TestParseDSFRating_MediaMonkey(t *testing.T) {
	data := makeDSF(t, makeTagWithTXXX(t, "FMPS_Rating", "1.0"))
	stars, ok := parseDSFRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)
}

func TestParseDSFRating_foobar2000(t *testing.T) {
	data := makeDSF(t, makeTagWithTXXX(t, "RATING", "3"))
	stars, ok := parseDSFRating(data, []string{"foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

func TestParseDSFRating_WMP(t *testing.T) {
	data := makeDSF(t, makeTagWithPOPM(t, "Windows Media Player 9 Series", 90))
	stars, ok := parseDSFRating(data, []string{"WMP"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

func TestParseDSFRating_iTunes(t *testing.T) {
	data := makeDSF(t, makeTagWithPOPM(t, "iTunes", 80))
	stars, ok := parseDSFRating(data, []string{"iTunes"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

// ─── tag order ────────────────────────────────────────────────────────────────

func TestParseDSFRating_TagOrderRespected(t *testing.T) {
	id3Data := makeTagWithTXXX(t, "FMPS_Rating", "0.2")
	data := makeDSF(t, id3Data)

	stars, ok := parseDSFRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 1, stars)

	_, ok = parseDSFRating(data, []string{"WMP"})
	assert.False(t, ok)
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractDSFMetadata_SkipsSamples proves the DSF extractor follows the
// id3 offset in the DSD header DIRECTLY — never reading the (potentially
// gigabytes of) DSD samples sitting between header and tag. The sentinel
// lives in the samples region; if the extractor ever read instead of
// seeking, the synth output would include it.
func TestExtractDSFMetadata_SkipsSamples(t *testing.T) {
	dir := t.TempDir()
	id3Body := realID3v2(t, "0.4") // 2 stars

	samples := junkAudio()
	id3Off := uint64(28 + len(samples))

	var w bytes.Buffer
	w.WriteString("DSD ")
	w.Write(make([]byte, 16)) // bytes 4..19 placeholder
	off := make([]byte, 8)
	binary.LittleEndian.PutUint64(off, id3Off)
	w.Write(off)     // bytes 20..27 = ID3 offset
	w.Write(samples) // DSD audio samples (sentinel hides here)
	w.Write(id3Body) // ID3 tag at the offset

	path := writeBinFile(t, dir, "song.dsf", w.Bytes())

	data := extractedBytes(t, path, "dsf")
	assert.NotContains(t, string(data), string(sentinel),
		"DSF extractor must Seek directly to the ID3 offset")

	stars, result := extractStarsFromFile(path, "dsf", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 2, stars)
}
