package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogem/id3v2/v2"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
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
	path := writeFMPSFile(t, "0.6")
	stars, ok := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.True(t, ok)
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

	stars, ok := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 4, stars)
}

// ─── checkAndRunUserTriggeredScanWith ─────────────────────────────────────────

func TestCheckAndRunUserTriggeredScanWith_NoTriggerUsers(t *testing.T) {
	resetSubsonicMock(t)
	err := checkAndRunUserTriggeredScanWith(pluginConfig{})
	assert.NoError(t, err)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
}

func TestCheckAndRunUserTriggeredScanWith_CooldownSkipsUser(t *testing.T) {
	resetSubsonicMock(t)

	const username = "alice"
	lastUserScanMu.Lock()
	lastUserScanTimes[username] = time.Now()
	lastUserScanMu.Unlock()
	t.Cleanup(func() {
		lastUserScanMu.Lock()
		delete(lastUserScanTimes, username)
		lastUserScanMu.Unlock()
	})

	cfg := pluginConfig{
		UserScanCooldownHours: 24,
		Libraries: []libraryConfig{{
			LibraryID: "lib1",
			Users: []userConfig{{
				Username:        username,
				TriggerUserScan: true,
				RatingTagOrder:  defaultTagOrder,
			}},
		}},
	}

	err := checkAndRunUserTriggeredScanWith(cfg)
	assert.NoError(t, err)
	// The actual invariant: cooldown means no Subsonic traffic at all.
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
}

// ─── runSyncWith ─────────────────────────────────────────────────────────────

func TestRunSyncWith_NoLibraries(t *testing.T) {
	err := runSyncWith(pluginConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

// ─── runSyncForUser happy path (integration) ─────────────────────────────────

// TestRunSyncForUser_HappyPath wires the full pipeline together: SubsonicAPI
// returns one song pointing at a real ID3-tagged temp file, the scanner reads
// the rating, and setRating is called with the correct star count.
func TestRunSyncForUser_HappyPath(t *testing.T) {
	resetSubsonicMock(t)

	// 1. Real ID3 file with FMPS_Rating=0.6 (→ 3 stars).
	path := writeFMPSFile(t, "0.6")

	// 2. fetchAllSongs returns one song pointing at that path.
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: path, Suffix: "mp3"}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)

	// 3. setRating expects rating=3.
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{
		Username:         "alice",
		SkipAlreadyRated: true,
		RatingTagOrder:   []string{"MediaMonkey"},
	}

	err := runSyncForUser(lib, user, 0)
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
}

// writeFMPSFile creates a temp .mp3 with an FMPS_Rating TXXX frame.
func writeFMPSFile(t *testing.T, value string) string {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: value,
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.mp3")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}