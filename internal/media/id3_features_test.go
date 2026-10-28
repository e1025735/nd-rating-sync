//go:build !tinygo

package media

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Hand-built ID3v2 tags with features the tag library does not implement.

func syncsafe(n int) []byte {
	return []byte{byte(n >> 21 & 0x7F), byte(n >> 14 & 0x7F), byte(n >> 7 & 0x7F), byte(n & 0x7F)}
}

// wmpPOPM is a POPM body worth 4 stars for the WMP key.
func wmpPOPM() []byte {
	return append([]byte("Windows Media Player 9 Series\x00"), 196, 0, 0, 0, 0)
}

func v23Frame(id string, format byte, body []byte) []byte {
	out := []byte(id)
	out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
	out = append(out, 0, format)
	return append(out, body...)
}

func v24Frame(id string, format byte, body []byte) []byte {
	out := append([]byte(id), syncsafe(len(body))...)
	out = append(out, 0, format)
	return append(out, body...)
}

func id3Tag(version, flags byte, frames ...[]byte) []byte {
	body := bytes.Join(frames, nil)
	out := []byte{'I', 'D', '3', version, 0, flags}
	out = append(out, syncsafe(len(body))...)
	return append(out, body...)
}

// An extended header is cut out, so the frames behind it are read. The v2.3
// case used to come back silently as "no tag".
func TestRatingFromID3v2_ExtendedHeaderIsSkipped(t *testing.T) {
	cases := map[string][]byte{
		"v2.3": id3Tag(3, 0x40, []byte{0, 0, 0, 6, 0, 0, 0, 0, 0, 0}, v23Frame("POPM", 0, wmpPOPM())),
		"v2.4": id3Tag(4, 0x40, []byte{0, 0, 0, 6, 1, 0}, v24Frame("POPM", 0, wmpPOPM())),
	}
	for name, data := range cases {
		stars, found, err := ratingFromID3v2(data, "", []string{"WMP"})
		assert.NoError(t, err, name)
		assert.True(t, found, name)
		assert.Equal(t, 4, stars, name)
	}
}

func TestRatingFromID3v2_BrokenExtendedHeaderIsError(t *testing.T) {
	data := id3Tag(3, 0x40, []byte{0, 0, 0x10, 0}, v23Frame("POPM", 0, wmpPOPM()))
	_, found, err := ratingFromID3v2(data, "", []string{"WMP"})
	assert.False(t, found)
	assert.Error(t, err)
}

// A rating frame the library would misread as "no rating" makes the file
// unreadable instead.
func TestRatingFromID3v2_UnsupportedRatingFrameIsError(t *testing.T) {
	popm := wmpPOPM()
	cases := map[string][]byte{
		"v2.4 data-length indicator": id3Tag(4, 0, v24Frame("POPM", 0x01, append(syncsafe(len(popm)), popm...))),
		"v2.4 compressed":            id3Tag(4, 0, v24Frame("POPM", 0x09, append(syncsafe(len(popm)), 0x78, 0x9C))),
		"v2.4 encrypted":             id3Tag(4, 0, v24Frame("POPM", 0x04, append([]byte{1}, popm...))),
		"v2.4 grouped TXXX":          id3Tag(4, 0, v24Frame("TXXX", 0x40, append([]byte{1}, []byte("\x03RATING\x004")...))),
		"v2.3 compressed":            id3Tag(3, 0, v23Frame("POPM", 0x80, append([]byte{0, 0, 0, 40}, 0x78, 0x9C))),
		"v2.3 encrypted":             id3Tag(3, 0, v23Frame("POPM", 0x40, append([]byte{1}, popm...))),
	}
	for name, data := range cases {
		_, found, err := ratingFromID3v2(data, "", []string{"WMP", "foobar2000"})
		assert.False(t, found, name)
		assert.Error(t, err, name)
	}
}

// Flags on frames that cannot carry a rating do not matter: the library skips
// those frames by their size.
func TestRatingFromID3v2_FlagsOnOtherFramesAreIgnored(t *testing.T) {
	data := id3Tag(4, 0,
		v24Frame("TIT2", 0x09, append(syncsafe(5), 0x78, 0x9C, 1, 2, 3)),
		v24Frame("POPM", 0, wmpPOPM()))
	stars, found, err := ratingFromID3v2(data, "", []string{"WMP"})
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 4, stars)
}
