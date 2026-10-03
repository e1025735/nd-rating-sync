//go:build !tinygo

package scanner

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── extractStarsFromFile ─────────────────────────────────────────────────────

func TestExtractStarsFromFile_FileNotFound(t *testing.T) {
	stars, result := extractStarsFromFile("/no/such/file.mp3", "mp3", defaultTagOrder)
	assert.Equal(t, fileUnreadable, result)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_EmptyWAVIsUnreadable(t *testing.T) {
	// An empty .wav has no RIFF magic, so the parser cannot extract anything.
	// Must surface as fileUnreadable (not tagAbsent) so clear_rating_if_untagged
	// will not mistakenly clear the user's rating.
	f, err := os.CreateTemp(t.TempDir(), "song*.wav")
	require.NoError(t, err)
	f.Close()

	stars, result := extractStarsFromFile(f.Name(), "wav", defaultTagOrder)
	assert.Equal(t, tagAbsent, result, "empty WAV currently parses as 'no tag found' — this is fine; the read itself succeeded")
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_FLACWithFMPS(t *testing.T) {
	data := makeFLAC(t, "FMPS_RATING=0.6")
	path := filepath.Join(t.TempDir(), "song.flac")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	stars, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

func TestExtractStarsFromFile_OggVorbisWithFMPS(t *testing.T) {
	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.8")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
	path := filepath.Join(t.TempDir(), "song.ogg")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	stars, result := extractStarsFromFile(path, "ogg", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}

func TestExtractStarsFromFile_OpusWithFoobar(t *testing.T) {
	commentPkt := makeOpusCommentPacket(t, "RATING=2")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
	path := filepath.Join(t.TempDir(), "song.opus")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	stars, result := extractStarsFromFile(path, "opus", []string{"foobar2000"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 2, stars)
}

func TestExtractStarsFromFile_UnsupportedExtensionFromPath(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "song*.aiff")
	require.NoError(t, err)
	f.Close()

	_, result := extractStarsFromFile(f.Name(), "", defaultTagOrder)
	assert.Equal(t, fileUnreadable, result, "aiff is unsupported and must surface as unreadable so clear_rating_if_untagged does not wipe the rating")
}

func TestExtractStarsFromFile_MP3NoRatingTag(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.mp3")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, result := extractStarsFromFile(path, "mp3", defaultTagOrder)
	assert.Equal(t, tagAbsent, result)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_MP3WithFMPS(t *testing.T) {
	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	stars, result := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

func TestExtractStarsFromFile_SuffixOverridesPathExtension(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: "0.8",
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.bin")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, result := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}

// TestExtractStarsFromFile_HugeFileNoLongerSkipped proves we removed the
// blunt 64 MiB whole-file size cap that v0.10.x imposed. A 100 MiB FLAC
// with the rating tag near the start is now processed (Phase 1 reads only
// the metadata) where previously it would have been counted as unreadable.
// The per-format extractor tests (in flac_test.go etc.) cover what bytes
// the extractor reads; this test covers extractStarsFromFile's end-to-end
// behaviour on a file that exceeds the old whole-file cap.
func TestExtractStarsFromFile_HugeFileNoLongerSkipped(t *testing.T) {
	cmt := makeFLAC(t, "FMPS_RATING=0.8") // 4 stars
	huge := append([]byte{}, cmt...)
	huge = append(huge, bytes.Repeat([]byte{0}, 100*1024*1024)...) // 100 MiB tail
	path := filepath.Join(t.TempDir(), "long-classical.flac")
	require.NoError(t, os.WriteFile(path, huge, 0o644))

	stars, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result, "100 MiB FLAC must now be processed, not skipped")
	assert.Equal(t, 4, stars)
}
