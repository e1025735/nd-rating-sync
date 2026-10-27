//go:build !tinygo

package scanner

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

func writeEmptyID3MP3At(t *testing.T, dir, name string) string {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

func TestProcessSong_SkipsAlreadyRated(t *testing.T) {
	resetSubsonicMock(t)

	tally := &syncTally{}
	song := subsonicSong{
		ID:         "song-1",
		Title:      "Already rated",
		Suffix:     "mp3",
		UserRating: 5,
	}

	processSong(
		UserConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder},
		PluginConfig{},
		song,
		time.Time{},
		songFile{},
		tally,
	)

	assert.Equal(t, 1, tally.skippedRated)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=0&u=alice")
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=5&u=alice")
}

func TestProcessSong_UnlocatedFileCountsUnreadable(t *testing.T) {
	resetSubsonicMock(t)

	tally := &syncTally{}
	song := subsonicSong{
		ID:     "song-2",
		Title:  "Missing file",
		Suffix: "mp3",
		Size:   123,
	}

	processSong(
		UserConfig{Username: "alice", RatingTagOrder: DefaultTagOrder},
		PluginConfig{},
		song,
		time.Time{},
		songFile{reason: "Navidrome returned no track for this song ID"},
		tally,
	)

	assert.Equal(t, 1, tally.skippedUnreadable)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-2&rating=0&u=alice")
}

func TestProcessSong_ClearsRatingWhenUntaggedAndAllowed(t *testing.T) {
	resetSubsonicMock(t)

	dir := t.TempDir()
	path := writeEmptyID3MP3At(t, dir, "song.mp3")
	size := fileSize(t, path)
	file := songFile{entry: FileEntry{Path: path, Size: size, MTime: time.Now()}, found: true}

	host.SubsonicAPIMock.On("Call", "setRating?id=song-3&rating=0&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	tally := &syncTally{}
	song := subsonicSong{
		ID:     "song-3",
		Title:  "Needs clearing",
		Suffix: "mp3",
		Size:   size,
	}

	processSong(
		UserConfig{
			Username:              "alice",
			ClearRatingIfUntagged: true,
			RatingTagOrder:        DefaultTagOrder,
		},
		PluginConfig{},
		song,
		time.Time{},
		file,
		tally,
	)

	assert.Equal(t, 1, tally.cleared)
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestProcessSong_DryRunWouldRateTaggedFile(t *testing.T) {
	resetSubsonicMock(t)

	dir := t.TempDir()
	path := writeFMPSFileAt(t, dir, "song.mp3", "0.6")
	size := fileSize(t, path)
	file := songFile{entry: FileEntry{Path: path, Size: size, MTime: time.Now()}, found: true}

	tally := &syncTally{}
	song := subsonicSong{
		ID:     "song-4",
		Title:  "Would rate",
		Suffix: "mp3",
		Size:   size,
	}

	processSong(
		UserConfig{Username: "alice", RatingTagOrder: []string{"MediaMonkey"}},
		PluginConfig{DryRun: true},
		song,
		time.Time{},
		file,
		tally,
	)

	assert.Equal(t, 1, tally.wouldRate)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-4&rating=3&u=alice")
}

// A renamed/moved file keeps its old mtime, but Navidrome imports it as a new
// song. Incremental sync must still read it instead of counting it unchanged.
func TestProcessSong_IncrementalProcessesNewlyAddedSongWithOldMTime(t *testing.T) {
	resetSubsonicMock(t)

	dir := t.TempDir()
	path := writeFMPSFileAt(t, dir, "renamed.mp3", "0.6")
	size := fileSize(t, path)
	threshold := time.Now().Add(-time.Hour)
	file := songFile{entry: FileEntry{Path: path, Size: size, MTime: threshold.Add(-24 * time.Hour)}, found: true}

	tally := &syncTally{}
	song := subsonicSong{
		ID:      "song-5",
		Title:   "Renamed",
		Suffix:  "mp3",
		Size:    size,
		Created: threshold.Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}

	processSong(
		UserConfig{Username: "alice", RatingTagOrder: []string{"MediaMonkey"}},
		PluginConfig{DryRun: true},
		song,
		threshold,
		file,
		tally,
	)

	assert.Equal(t, 0, tally.skippedUnchanged)
	assert.Equal(t, 1, tally.wouldRate)
}

func TestProcessSong_IncrementalSkipsOldSongWithOldMTime(t *testing.T) {
	resetSubsonicMock(t)

	threshold := time.Now().Add(-time.Hour)
	old := threshold.Add(-24 * time.Hour)
	file := songFile{entry: FileEntry{Path: "/nonexistent.mp3", Size: 42, MTime: old}, found: true}

	for _, created := range []string{"", "not-a-date", old.UTC().Format(time.RFC3339Nano)} {
		tally := &syncTally{}
		song := subsonicSong{ID: "song-6", Title: "Old", Suffix: "mp3", Size: 42, Created: created}

		processSong(
			UserConfig{Username: "alice", RatingTagOrder: DefaultTagOrder},
			PluginConfig{},
			song,
			threshold,
			file,
			tally,
		)

		assert.Equal(t, 1, tally.skippedUnchanged, "created=%q", created)
	}
}
