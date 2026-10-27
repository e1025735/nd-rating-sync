//go:build !tinygo

package scanner

import (
	"os"
	"path"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelativeTrackPath(t *testing.T) {
	for _, tc := range []struct {
		name, root, path, want string
		ok                     bool
	}{
		{"relative", "/music", "Artist/Album/01.flac", "Artist/Album/01.flac", true},
		{"leading slash below root", "/music", "/music/Artist/01.flac", "Artist/01.flac", true},
		{"root with trailing slash", "/music/", "/music/Artist/01.flac", "Artist/01.flac", true},
		{"absolute outside root", "/music", "/other/01.flac", "", false},
		{"sibling with root as prefix", "/music", "/music2/01.flac", "", false},
		{"absolute without known root", "", "/music/01.flac", "", false},
		{"escapes the root", "/music", "../etc/passwd", "", false},
		{"escapes after cleaning", "/music", "Artist/../../x.mp3", "", false},
		{"cleaned inside root", "/music", "Artist/./Album/../01.mp3", "Artist/01.mp3", true},
		{"empty", "/music", "", "", false},
		{"backslash is a file name character on Unix", "/music", `AC\DC/01.mp3`, `AC\DC/01.mp3`, true},
		{"Windows relative", `D:\Music`, `Artist\Album\01.flac`, "Artist/Album/01.flac", true},
		{"Windows absolute, other case", `D:\Music`, `d:\music\Artist\01.flac`, "Artist/01.flac", true},
		{"Windows absolute outside root", `D:\Music`, `E:\Other\01.flac`, "", false},
	} {
		got, ok := relativeTrackPath(tc.root, tc.path)
		assert.Equal(t, tc.ok, ok, tc.name)
		assert.Equal(t, tc.want, got, tc.name)
	}
}

func TestLocateTrack_FindsFileInsideMount(t *testing.T) {
	mount := t.TempDir()
	require.NoError(t, os.MkdirAll(mount+"/Artist", 0o755))
	full := path.Join(mount, "Artist", "01.mp3")
	require.NoError(t, os.WriteFile(full, []byte("12345"), 0o644))
	when := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	require.NoError(t, os.Chtimes(full, when, when))

	m := libraryMount{id: 1, mountPoint: mount, hostPath: hostLibraryRoot}
	f := locateTrack(m, "s1", trackAt("s1", 1, "Artist/01.mp3"))
	require.True(t, f.found, f.reason)
	assert.Equal(t, full, f.entry.Path)
	assert.True(t, when.Equal(f.entry.MTime), "mtime comes from the file itself")
}

func TestLocateTrack_ReportsWhyAFileIsNotLocated(t *testing.T) {
	m := libraryMount{id: 1, mountPoint: t.TempDir(), hostPath: hostLibraryRoot}
	for name, tr := range map[string]*types.Track{
		"no track":       nil,
		"other song":     trackAt("s2", 1, "a.mp3"),
		"missing":        {ID: "s1", LibraryID: 1, Path: "a.mp3", Missing: true},
		"other library":  trackAt("s1", 2, "a.mp3"),
		"no path":        trackAt("s1", 1, ""),
		"outside root":   trackAt("s1", 1, "/elsewhere/a.mp3"),
		"file not there": trackAt("s1", 1, "a.mp3"),
	} {
		f := locateTrack(m, "s1", tr)
		assert.False(t, f.found, name)
		assert.NotEmpty(t, f.reason, name)
	}
}

func TestLocateSongFiles_MatcherShortResultCountsAsNotFound(t *testing.T) {
	resetMatcherMock(t)
	mockMatch([]string{"a", "b"}) // matcher returned nothing at all

	files, err := locateSongFiles(libraryMount{id: 1, mountPoint: t.TempDir()}, UserConfig{}, []subsonicSong{{ID: "a"}, {ID: "b"}})
	require.NoError(t, err)
	assert.False(t, files["a"].found)
	assert.False(t, files["b"].found)
}
