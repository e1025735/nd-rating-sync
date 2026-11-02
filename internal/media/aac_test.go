//go:build !tinygo

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

var adtsFrame = []byte{0xFF, 0xF1, 0x50, 0x80, 0x2E, 0x7F, 0xFC, 0, 0, 0, 0}

// An ADTS stream with a leading ID3v2 tag is read like an MP3 tag.
func TestExtractAACMetadata_ID3TaggedADTS(t *testing.T) {
	content := append(makeTagWithPOPM(t, "Windows Media Player 9 Series", 196), adtsFrame...)
	path := writeBinFile(t, t.TempDir(), "song.aac", content)

	stars, result := extractStarsFromFile(path, "aac", []string{"WMP"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}

// An ID3v2 tag without a rating is a clean read: untagged, as for MP3.
func TestExtractAACMetadata_ID3TagWithoutRatingIsUntagged(t *testing.T) {
	content := append(makeTagWithTXXX(t, "SOMETHING", "else"), adtsFrame...)
	path := writeBinFile(t, t.TempDir(), "song.aac", content)

	_, result := extractStarsFromFile(path, "aac", []string{"WMP", "MediaMonkey", "foobar2000"})
	assert.Equal(t, tagAbsent, result)
}

// An MP4 file named .aac is read like an .m4a.
func TestExtractAACMetadata_MP4NamedAAC(t *testing.T) {
	path := writeBinFile(t, t.TempDir(), "song.aac", buildM4A(map[string]string{"RATE": "60"}))

	stars, result := extractStarsFromFile(path, "aac", []string{"MusicBee"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

func TestIsADTSHeader(t *testing.T) {
	assert.True(t, isADTSHeader([]byte{0xFF, 0xF1}), "MPEG-4, no CRC")
	assert.True(t, isADTSHeader([]byte{0xFF, 0xF9}), "MPEG-2, no CRC")
	assert.False(t, isADTSHeader([]byte{0xFF, 0xFB}), "layer bits set: MP3, not ADTS")
	assert.False(t, isADTSHeader([]byte{0xFF}))
}
