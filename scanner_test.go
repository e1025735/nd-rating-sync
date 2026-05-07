package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogem/id3v2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── extractStarsFromFile ─────────────────────────────────────────────────────

func TestExtractStarsFromFile_FileNotFound(t *testing.T) {
	stars, ok := extractStarsFromFile("/no/such/file.mp3", "mp3", defaultTagOrder)
	assert.False(t, ok)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_UnsupportedFormat(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "song*.flac")
	require.NoError(t, err)
	f.Close()

	stars, ok := extractStarsFromFile(f.Name(), "flac", defaultTagOrder)
	assert.False(t, ok)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_ExtensionFromPath(t *testing.T) {
	// When suffix is empty the extension is derived from the file path.
	f, err := os.CreateTemp(t.TempDir(), "song*.ogg")
	require.NoError(t, err)
	f.Close()

	_, ok := extractStarsFromFile(f.Name(), "", defaultTagOrder)
	assert.False(t, ok, "ogg should be skipped as unsupported")
}

func TestExtractStarsFromFile_MP3NoRatingTag(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.mp3")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, ok := extractStarsFromFile(path, "mp3", defaultTagOrder)
	assert.False(t, ok)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_MP3WithFMPS(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: "FMPS_Rating",
		Value:       "0.6",
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.mp3")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, ok := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

func TestExtractStarsFromFile_SuffixOverridesPathExtension(t *testing.T) {
	// File named .bin but suffix="mp3" forces ID3 parsing.
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: "FMPS_Rating",
		Value:       "0.8",
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.bin")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, ok := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

// ─── checkAndRunUserTriggeredScan ─────────────────────────────────────────────

func TestCheckAndRunUserTriggeredScan_NoTriggerUsers(t *testing.T) {
	withConfig(t, map[string]string{})
	err := checkAndRunUserTriggeredScan()
	assert.NoError(t, err)
}

func TestCheckAndRunUserTriggeredScan_CooldownPreventsRepeat(t *testing.T) {
	const username = "alice"

	lastUserScanMu.Lock()
	lastUserScanTimes[username] = time.Now()
	lastUserScanMu.Unlock()
	t.Cleanup(func() {
		lastUserScanMu.Lock()
		delete(lastUserScanTimes, username)
		lastUserScanMu.Unlock()
	})

	withConfig(t, map[string]string{
		"user_scan_cooldown_hours": "24",
		"libraries":                `[{"libraryId":"lib1","users":[{"username":"alice","trigger_user_scan":true}]}]`,
	})

	// Should return nil because the cooldown is still active.
	err := checkAndRunUserTriggeredScan()
	assert.NoError(t, err)
}

// ─── runSync ─────────────────────────────────────────────────────────────────

func TestRunSync_NoLibraries(t *testing.T) {
	withConfig(t, map[string]string{})
	err := runSync()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}
