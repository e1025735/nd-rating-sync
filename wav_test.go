package main

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeWAV wraps id3Data in a minimal RIFF/WAVE container with a single "id3 " chunk.
func makeWAV(t *testing.T, id3Data []byte) []byte {
	t.Helper()
	var chunk bytes.Buffer
	chunk.WriteString("id3 ")
	var sz [4]byte
	binary.LittleEndian.PutUint32(sz[:], uint32(len(id3Data)))
	chunk.Write(sz[:])
	chunk.Write(id3Data)
	if len(id3Data)%2 != 0 {
		chunk.WriteByte(0) // RIFF even-boundary padding
	}

	var out bytes.Buffer
	out.WriteString("RIFF")
	var fileSz [4]byte
	binary.LittleEndian.PutUint32(fileSz[:], uint32(4+chunk.Len()))
	out.Write(fileSz[:])
	out.WriteString("WAVE")
	out.Write(chunk.Bytes())
	return out.Bytes()
}

// makeWAVUpperCase is like makeWAV but writes "ID3 " (uppercase) as the fourCC.
func makeWAVUpperCase(t *testing.T, id3Data []byte) []byte {
	t.Helper()
	data := makeWAV(t, id3Data)
	copy(data[12:16], "ID3 ")
	return data
}

// ─── invalid / edge cases ─────────────────────────────────────────────────────

func TestParseWAVRating_EmptySlice(t *testing.T) {
	_, ok := parseWAVRating([]byte{}, []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWAVRating_InvalidRIFFMagic(t *testing.T) {
	_, ok := parseWAVRating([]byte("JUNK\x00\x00\x00\x00WAVE"), []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWAVRating_NotWAVE(t *testing.T) {
	_, ok := parseWAVRating([]byte("RIFF\x04\x00\x00\x00AIFF"), []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWAVRating_NoID3Chunk(t *testing.T) {
	// Valid WAVE header with no chunks.
	_, ok := parseWAVRating([]byte("RIFF\x04\x00\x00\x00WAVE"), []string{"WMP"})
	assert.False(t, ok)
}

func TestParseWAVRating_TruncatedChunkBody(t *testing.T) {
	var out bytes.Buffer
	out.WriteString("RIFF")
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint32(12)))
	out.WriteString("WAVE")
	out.WriteString("id3 ")
	require.NoError(t, binary.Write(&out, binary.LittleEndian, uint32(999))) // claims 999 but file ends here
	_, ok := parseWAVRating(out.Bytes(), []string{"WMP"})
	assert.False(t, ok)
}

// ─── tag source coverage ──────────────────────────────────────────────────────

func TestParseWAVRating_MediaMonkey(t *testing.T) {
	data := makeWAV(t, makeTagWithTXXX(t, "FMPS_Rating", "0.6"))
	stars, ok := parseWAVRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

func TestParseWAVRating_foobar2000(t *testing.T) {
	data := makeWAV(t, makeTagWithTXXX(t, "RATING", "4"))
	stars, ok := parseWAVRating(data, []string{"foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

func TestParseWAVRating_WMP(t *testing.T) {
	data := makeWAV(t, makeTagWithPOPM(t, "Windows Media Player 9 Series", 196))
	stars, ok := parseWAVRating(data, []string{"WMP"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

func TestParseWAVRating_iTunes(t *testing.T) {
	data := makeWAV(t, makeTagWithPOPM(t, "iTunes", 100))
	stars, ok := parseWAVRating(data, []string{"iTunes"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)
}

// ─── fourCC case insensitivity ────────────────────────────────────────────────

func TestParseWAVRating_UpperCaseFourCC(t *testing.T) {
	data := makeWAVUpperCase(t, makeTagWithTXXX(t, "FMPS_Rating", "0.8"))
	stars, ok := parseWAVRating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

// ─── tag order ────────────────────────────────────────────────────────────────

func TestParseWAVRating_TagOrderRespected(t *testing.T) {
	id3Data := makeTagWithTXXXAndPOPM(t,
		id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: "0.4"},
		id3v2.PopularimeterFrame{Email: "Windows Media Player 9 Series", Rating: 255, Counter: big.NewInt(0)},
	)
	data := makeWAV(t, id3Data)

	stars, ok := parseWAVRating(data, []string{"WMP", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)

	stars, ok = parseWAVRating(data, []string{"MediaMonkey", "WMP"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractWAVMetadata_SkipsDataChunk proves the WAV extractor Seeks past
// the `data` chunk (audio samples — usually the largest part of the file)
// and only reads the `id3 ` chunk body. The data chunk's body holds the
// sentinel; if the extractor reads through instead of seeking past, the
// returned synth would include it.
func TestExtractWAVMetadata_SkipsDataChunk(t *testing.T) {
	dir := t.TempDir()
	id3Body := realID3v2(t, "0.8") // 4 stars
	dataBody := junkAudio()

	var w bytes.Buffer
	w.WriteString("RIFF")
	w.Write([]byte{0, 0, 0, 0}) // RIFF size placeholder
	w.WriteString("WAVE")
	// fmt chunk (16 byte placeholder body)
	w.WriteString("fmt ")
	fmtBody := make([]byte, 16)
	require.NoError(t, binary.Write(&w, binary.LittleEndian, uint32(len(fmtBody))))
	w.Write(fmtBody)
	// giant data chunk
	w.WriteString("data")
	require.NoError(t, binary.Write(&w, binary.LittleEndian, uint32(len(dataBody))))
	w.Write(dataBody)
	// id3 chunk LAST so the extractor must skip data first
	w.WriteString("id3 ")
	require.NoError(t, binary.Write(&w, binary.LittleEndian, uint32(len(id3Body))))
	w.Write(id3Body)

	path := writeBinFile(t, dir, "song.wav", w.Bytes())

	data := extractedBytes(t, path, "wav")
	assert.NotContains(t, string(data), string(sentinel),
		"WAV extractor must Seek past the data chunk")

	stars, result := extractStarsFromFile(path, "wav", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}
