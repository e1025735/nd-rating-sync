//go:build !tinygo

package media

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/require"
)

// These helpers mirror the legacy root-level test utilities so the media tests
// can live under the media adapter package without depending on root-only
// unexported symbols.
type fileReadResult int

const (
	tagFound fileReadResult = iota
	tagAbsent
	fileUnreadable
)

var sentinel = []byte("NDRATINGSYNC_AUDIO_BODY_DO_NOT_READ")

const junkAudioBytes = 4 * 1024 * 1024

func junkAudio() []byte {
	out := make([]byte, junkAudioBytes)
	copy(out, sentinel)
	return out
}

func writeBinFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, content, 0o644))
	return p
}

func extractedBytes(t *testing.T, path, ext string) []byte {
	t.Helper()
	data, ok := readAudioMetadata(path, ext)
	require.True(t, ok, "readAudioMetadata must succeed for %q", path)
	return data
}

func realID3v2(t *testing.T, value string) []byte {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: "FMPS_Rating",
		Value:       value,
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func isSupportedExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4", "wma":
		return true
	default:
		return false
	}
}

func readAudioMetadata(path, ext string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	data, err := ExtractMetadata(ext, f)
	if err != nil {
		return nil, false
	}
	return data, true
}

// extractStarsFromFile mirrors the scanner's pipeline: unreadable on any read
// or parse error, tagAbsent when the tag carries no recognised rating.
func extractStarsFromFile(path string, suffix string, tagOrder []string) (int, fileReadResult) {
	ext := strings.ToLower(suffix)
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	if !isSupportedExt(ext) {
		return 0, fileUnreadable
	}

	data, ok := readAudioMetadata(path, ext)
	if !ok {
		return 0, fileUnreadable
	}

	stars, found, err := ParseRating(ext, data, path, tagOrder)
	if err != nil {
		return 0, fileUnreadable
	}
	if found {
		return stars, tagFound
	}
	return 0, tagAbsent
}
