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
		UserConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
		PluginConfig{},
		song,
		time.Time{},
		nil,
		false,
		"lib1",
		nil,
		tally,
	)

	assert.Equal(t, 1, tally.skippedRated)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=0&u=alice")
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=5&u=alice")
}

func TestProcessSong_MissingUniqueMatchCountsUnreadable(t *testing.T) {
	resetSubsonicMock(t)

	tally := &syncTally{}
	song := subsonicSong{
		ID:     "song-2",
		Title:  "Missing file",
		Suffix: "mp3",
		Size:   123,
	}

	processSong(
		UserConfig{Username: "alice", RatingTagOrder: defaultTagOrder},
		PluginConfig{},
		song,
		time.Time{},
		map[string][]FileEntry{},
		false,
		"lib1",
		nil,
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
	index := map[string][]FileEntry{
		sizeKey(size, "mp3"): {{Path: path, Size: size, MTime: time.Now()}},
	}

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
			RatingTagOrder:        defaultTagOrder,
		},
		PluginConfig{},
		song,
		time.Time{},
		index,
		false,
		"lib1",
		nil,
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
	index := map[string][]FileEntry{
		sizeKey(size, "mp3"): {{Path: path, Size: size, MTime: time.Now()}},
	}

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
		index,
		false,
		"lib1",
		nil,
		tally,
	)

	assert.Equal(t, 1, tally.wouldRate)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-4&rating=3&u=alice")
}
