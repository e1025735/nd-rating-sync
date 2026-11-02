//go:build !tinygo

package media

import (
	"math/big"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/assert"
)

// foobar2000 writes its RATING field to MP3 as the Windows Media Player POPM
// frame, so the foobar2000 key falls back to it.
func TestParseID3v2Rating_foobar2000FallsBackToWMPPOPM(t *testing.T) {
	data := makeTagWithPOPM(t, "Windows Media Player 9 Series", 196)

	stars, ok := parseID3v2Rating(data, "", []string{"foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

// A TXXX "RATING" frame is foobar2000's own field and wins over the POPM
// fallback.
func TestParseID3v2Rating_foobar2000TXXXBeatsWMPPOPM(t *testing.T) {
	data := makeTagWithTXXXAndPOPM(t,
		id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: "RATING", Value: "2"},
		id3v2.PopularimeterFrame{Email: "Windows Media Player 9 Series", Rating: 255, Counter: big.NewInt(0)})

	stars, ok := parseID3v2Rating(data, "", []string{"foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}

// Other POPM emails are not foobar2000's.
func TestParseID3v2Rating_foobar2000IgnoresOtherPOPM(t *testing.T) {
	data := makeTagWithPOPM(t, "MusicBee", 196)

	_, ok := parseID3v2Rating(data, "", []string{"foobar2000"})
	assert.False(t, ok)
}

func TestFMPSKey_EveryContainer(t *testing.T) {
	cases := map[string]struct {
		ext  string
		data []byte
	}{
		"ID3v2 TXXX":     {"mp3", makeTagWithTXXX(t, "FMPS_Rating", "0.8")},
		"Vorbis comment": {"flac", makeFLAC(t, "FMPS_RATING=0.8")},
		"MP4 freeform":   {"m4a", buildM4A(map[string]string{"FMPS_Rating": "0.8"})},
		"ASF string": {"wma", buildWMA(t, []wmaDescriptor{
			{name: "FMPS_Rating", valueType: 0, value: encodeUTF16LE("0.8")},
		})},
	}
	for name, tc := range cases {
		stars, found, err := ParseRating(tc.ext, tc.data, "", []string{"FMPS"})
		assert.NoError(t, err, name)
		assert.True(t, found, name)
		assert.Equal(t, 4, stars, name)
	}
}

// The FMPS key reads only FMPS_Rating; MediaMonkey's own field stays
// MediaMonkey's.
func TestFMPSKey_IgnoresMediaMonkeyRATING(t *testing.T) {
	data := makeFLAC(t, "RATING=60", "FMPS_RATING=0.2")

	stars, ok := parseFLACRating(data, "", []string{"FMPS"})
	assert.True(t, ok)
	assert.Equal(t, 1, stars, "FMPS_RATING 0.2")

	stars, ok = parseFLACRating(data, "", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars, "MediaMonkey's RATING wins for MediaMonkey")

	_, ok = parseFLACRating(makeFLAC(t, "RATING=60"), "", []string{"FMPS"})
	assert.False(t, ok, "RATING alone is not FMPS")
}
