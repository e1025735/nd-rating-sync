//go:build !tinygo

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tag that starts with "ID3" but is cut off, larger than the read cap or
// has a broken size field must be unreadable, never untagged — otherwise
// clear_rating_if_untagged would wipe a rating we simply failed to read.
func TestExtractID3v2Metadata_CorruptTagIsUnreadable(t *testing.T) {
	tag := realID3v2(t, "0.6")
	cases := map[string][]byte{
		"cut off":        tag[:len(tag)-1],
		"over the cap":   {'I', 'D', '3', 3, 0, 0, 0x7F, 0x7F, 0x7F, 0x7F},
		"not syncsafe":   {'I', 'D', '3', 3, 0, 0, 0x80, 0, 0, 0},
		"header only 3+": {'I', 'D', '3', 3, 0, 0, 0, 0, 0, 20},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeBinFile(t, t.TempDir(), "song.mp3", content)
			stars, result := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
			assert.Equal(t, fileUnreadable, result)
			assert.Equal(t, 0, stars)
		})
	}
}

func TestExtractID3v2Metadata_NoMagicIsTagAbsent(t *testing.T) {
	path := writeBinFile(t, t.TempDir(), "song.mp3", []byte("not an id3 tag at all"))
	_, result := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.Equal(t, tagAbsent, result)
}

// ID3v2.2 is not supported by the tag library: the file is reported as
// unreadable instead of being treated as untagged.
func TestParseRating_UnsupportedID3VersionIsError(t *testing.T) {
	_, found, err := ParseRating("mp3", []byte{'I', 'D', '3', 2, 0, 0, 0, 0, 0, 0}, "", []string{"MediaMonkey"})
	assert.False(t, found)
	assert.Error(t, err)
}

// A FLAC whose metadata ends before a block flagged as last is cut off.
func TestExtractFLACMetadata_CutOffMetadataIsUnreadable(t *testing.T) {
	// fLaC + an empty PADDING block that is NOT flagged as last, then EOF.
	content := []byte{'f', 'L', 'a', 'C', 0x01, 0x00, 0x00, 0x00}
	path := writeBinFile(t, t.TempDir(), "song.flac", content)
	_, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, fileUnreadable, result)
}

func TestParseRating_CorruptFLACBlockIsError(t *testing.T) {
	// VORBIS_COMMENT block (last) claiming 255 bytes that are not there.
	data := []byte{'f', 'L', 'a', 'C', 0x84, 0x00, 0x00, 0xFF}
	_, found, err := ParseRating("flac", data, "", []string{"MediaMonkey"})
	assert.False(t, found)
	assert.Error(t, err)
}

func TestParseRating_CorruptVorbisCommentIsError(t *testing.T) {
	// A comment packet whose vendor length runs past the packet.
	comment := append([]byte{0x03, 'v', 'o', 'r', 'b', 'i', 's'}, 0xFF, 0xFF, 0x00, 0x00)
	data := oggSynth([]byte("ident"), comment)
	_, found, err := ParseRating("ogg", data, "", []string{"MediaMonkey"})
	assert.False(t, found)
	assert.Error(t, err)
}

// Empty data is what the extractors return for a file without the
// container's magic: untagged, not an error, for every format.
func TestParseRating_EmptyDataIsUntagged(t *testing.T) {
	for _, ext := range []string{"mp3", "flac", "ogg", "opus", "wav", "dsf", "m4a", "wma"} {
		t.Run(ext, func(t *testing.T) {
			_, found, err := ParseRating(ext, nil, "", []string{"MediaMonkey"})
			require.NoError(t, err)
			assert.False(t, found)
		})
	}
}

func TestParseRating_UnsupportedExtension(t *testing.T) {
	_, _, err := ParseRating("aiff", nil, "", nil)
	assert.Error(t, err)
}
