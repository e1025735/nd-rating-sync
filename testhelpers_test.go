package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/require"
)

// mapGetter returns a configGetter closure over a local map. Used by tests
// to feed loadConfigFrom and the *With variants without touching globals.
func mapGetter(m map[string]string) configGetter {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

// ─── Shared fixtures for the per-format extractor tests ───────────────────────
//
// The extractor tests (in id3_test.go, flac_test.go, …) prove that each
// extractXxxMetadata function reads only the metadata-bearing region of an
// audio file and never the audio body. The pattern in every test is:
//
//   1. Build a tiny valid metadata header in the format's binary layout.
//   2. Append `sentinel` and `junkAudioBytes`-1 zero bytes — the synthetic
//      "audio body" the extractor must NOT read.
//   3. Write the result to a temp file and call readAudioMetadata.
//   4. Assert (a) extraction succeeded and (b) the returned slice does NOT
//      contain the sentinel — proving the extractor used Seek to skip past
//      the audio instead of reading it.
//
// Lives in testhelpers_test.go so multiple format test files can reuse the
// fixtures without duplicating them or pulling them out of build-only files.

// sentinel is a byte sequence the extractors must never include in their
// output — it lives only in the synthetic "audio body" appended after the
// metadata region.
var sentinel = []byte("NDRATINGSYNC_AUDIO_BODY_DO_NOT_READ")

// junkAudioBytes is large enough that "read the whole file" is observably
// different from "read only metadata". 4 MiB makes the difference
// unambiguous in unit tests without bloating the test temp dir.
const junkAudioBytes = 4 * 1024 * 1024

// junkAudio returns sentinel followed by zero padding to junkAudioBytes —
// the sentinel sits at the very start of the junk so any extractor that
// reads even a few bytes past the metadata boundary catches it.
func junkAudio() []byte {
	out := make([]byte, junkAudioBytes)
	copy(out, sentinel)
	return out
}

// writeBinFile writes content to dir/name and returns the absolute path.
func writeBinFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, content, 0o644))
	return p
}

// extractedBytes pulls what readAudioMetadata returns for a given path/ext.
// Fails the test if extraction errored.
func extractedBytes(t *testing.T, path, ext string) []byte {
	t.Helper()
	data, ok := readAudioMetadata(path, ext)
	require.True(t, ok, "readAudioMetadata must succeed for %q", path)
	return data
}

// realID3v2 returns a real ID3v2 tag with FMPS_Rating=value.
func realID3v2(t *testing.T, value string) []byte {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: value,
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}
