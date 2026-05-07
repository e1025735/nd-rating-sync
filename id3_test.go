package main

import (
	"bytes"
	"testing"

	"math/big"
	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func makeTagWithTXXX(t *testing.T, description, value string) []byte {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: description,
		Value:       value,
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func makeTagWithPOPM(t *testing.T, email string, rating byte) []byte {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("POPM", id3v2.PopularimeterFrame{
		Email:   email,
		Rating:  rating,
		Counter: big.NewInt(0),
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func makeTagWithTXXXAndPOPM(t *testing.T, txxx id3v2.UserDefinedTextFrame, popm id3v2.PopularimeterFrame) []byte {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", txxx)
	tag.AddFrame("POPM", popm)
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

// ─── empty / invalid data ─────────────────────────────────────────────────────

func TestParseID3v2Rating_NoFrames(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	stars, ok := parseID3v2Rating(buf.Bytes(), []string{"WMP", "iTunes", "MediaMonkey"})
	assert.False(t, ok)
	assert.Equal(t, 0, stars)
}

func TestParseID3v2Rating_InvalidData(t *testing.T) {
	stars, ok := parseID3v2Rating([]byte("not an mp3 file"), []string{"WMP"})
	assert.False(t, ok)
	assert.Equal(t, 0, stars)
}

func TestParseID3v2Rating_EmptySlice(t *testing.T) {
	stars, ok := parseID3v2Rating([]byte{}, []string{"WMP"})
	assert.False(t, ok)
	assert.Equal(t, 0, stars)
}

// ─── MediaMonkey (FMPS_Rating TXXX) ──────────────────────────────────────────

func TestParseID3v2Rating_MediaMonkeyCanonicalValues(t *testing.T) {
	cases := []struct{ fmps string; want int }{
		{"0.2", 1}, {"0.4", 2}, {"0.6", 3}, {"0.8", 4}, {"1.0", 5},
	}
	for _, tc := range cases {
		data := makeTagWithTXXX(t, "FMPS_Rating", tc.fmps)
		stars, ok := parseID3v2Rating(data, []string{"MediaMonkey"})
		assert.True(t, ok, "FMPS_Rating=%s", tc.fmps)
		assert.Equal(t, tc.want, stars, "FMPS_Rating=%s", tc.fmps)
	}
}

func TestParseID3v2Rating_MediaMonkeyZeroIsUnrated(t *testing.T) {
	data := makeTagWithTXXX(t, "FMPS_Rating", "0.0")
	_, ok := parseID3v2Rating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseID3v2Rating_MediaMonkeyCaseInsensitiveDescription(t *testing.T) {
	data := makeTagWithTXXX(t, "FMPS_RATING", "0.6")
	stars, ok := parseID3v2Rating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

// ─── WMP (POPM) ───────────────────────────────────────────────────────────────

func TestParseID3v2Rating_WMPCanonicalValues(t *testing.T) {
	cases := []struct{ rating byte; want int }{
		{1, 1}, {25, 2}, {50, 3}, {75, 4}, {99, 5},
	}
	for _, tc := range cases {
		data := makeTagWithPOPM(t, "Windows Media Player 9 Series", tc.rating)
		stars, ok := parseID3v2Rating(data, []string{"WMP"})
		assert.True(t, ok, "WMP rating=%d", tc.rating)
		assert.Equal(t, tc.want, stars, "WMP rating=%d", tc.rating)
	}
}

func TestParseID3v2Rating_WMPZeroIsUnrated(t *testing.T) {
	data := makeTagWithPOPM(t, "Windows Media Player 9 Series", 0)
	_, ok := parseID3v2Rating(data, []string{"WMP"})
	assert.False(t, ok)
}

// ─── iTunes (POPM) ────────────────────────────────────────────────────────────

func TestParseID3v2Rating_iTunesCanonicalValues(t *testing.T) {
	cases := []struct {
		email  string
		rating byte
		want   int
	}{
		{"iTunes", 20, 1},
		{"iTunes", 40, 2},
		{"iTunes", 60, 3},
		{"iTunes", 80, 4},
		{"iTunes", 100, 5},
		{"com.apple.iTunes", 60, 3},
		{"COM.APPLE.ITUNES", 80, 4},
	}
	for _, tc := range cases {
		data := makeTagWithPOPM(t, tc.email, tc.rating)
		stars, ok := parseID3v2Rating(data, []string{"iTunes"})
		assert.True(t, ok, "email=%q rating=%d", tc.email, tc.rating)
		assert.Equal(t, tc.want, stars, "email=%q rating=%d", tc.email, tc.rating)
	}
}

func TestParseID3v2Rating_UnknownPOPMEmailIgnored(t *testing.T) {
	data := makeTagWithPOPM(t, "unknown@example.com", 80)
	_, ok := parseID3v2Rating(data, []string{"WMP", "iTunes", "MediaMonkey"})
	assert.False(t, ok)
}

// ─── tag order / priority ─────────────────────────────────────────────────────

func TestParseID3v2Rating_TagOrderWMPBeatsMediaMonkey(t *testing.T) {
	// FMPS → 3 stars; WMP POPM → 5 stars; tagOrder prefers WMP.
	data := makeTagWithTXXXAndPOPM(t,
		id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: "0.6"},
		id3v2.PopularimeterFrame{Email: "Windows Media Player 9 Series", Rating: 99, Counter: big.NewInt(0)},
	)
	stars, ok := parseID3v2Rating(data, []string{"WMP", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)
}

func TestParseID3v2Rating_TagOrderFallsThroughToMediaMonkey(t *testing.T) {
	// Only FMPS present; WMP listed first but not found → falls to MediaMonkey.
	data := makeTagWithTXXX(t, "FMPS_Rating", "0.4")
	stars, ok := parseID3v2Rating(data, []string{"WMP", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

func TestParseID3v2Rating_EmptyTagOrderNeverMatches(t *testing.T) {
	data := makeTagWithTXXX(t, "FMPS_Rating", "0.6")
	_, ok := parseID3v2Rating(data, []string{})
	assert.False(t, ok)
}
