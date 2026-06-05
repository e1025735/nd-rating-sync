package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSizeKey_CombinesSizeAndExt(t *testing.T) {
	assert.Equal(t, "123:mp3", sizeKey(123, "mp3"))
	assert.NotEqual(t, sizeKey(123, "mp3"), sizeKey(123, "flac"))
}

func TestIsSupportedExt(t *testing.T) {
	for _, ext := range []string{"mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4", "wma"} {
		assert.True(t, isSupportedExt(ext), ext)
	}
	// Unsupported, plus an uppercase form (callers lowercase before calling).
	for _, ext := range []string{"", "txt", "jpg", "nfo", "MP3"} {
		assert.False(t, isSupportedExt(ext), ext)
	}
}

func TestBuildFileIndex_IndexesSupportedFilesRecursivelyBySize(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.mp3"), []byte("12345"), 0o644)) // 5 bytes
	sub := filepath.Join(root, "Artist", "Album")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "b.flac"), []byte("1234567"), 0o644)) // 7 bytes
	// Unsupported extension must be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("xxxxxxxxxx"), 0o644))

	index, err := buildFileIndex(root)
	require.NoError(t, err)
	assert.Len(t, index, 2, "only the two supported files should be indexed")

	mp3 := index[sizeKey(5, "mp3")]
	require.Len(t, mp3, 1)
	assert.Equal(t, filepath.Join(root, "a.mp3"), mp3[0].path)

	flac := index[sizeKey(7, "flac")]
	require.Len(t, flac, 1)
	assert.Equal(t, filepath.Join(sub, "b.flac"), flac[0].path)
}

func TestBuildFileIndex_MissingRootIsError(t *testing.T) {
	_, err := buildFileIndex(filepath.Join(t.TempDir(), "does-not-exist"))
	assert.Error(t, err)
}

func TestMatchFile_UniqueSizeAndSuffix(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "song.mp3")
	require.NoError(t, os.WriteFile(p, []byte("12345"), 0o644))
	index, err := buildFileIndex(root)
	require.NoError(t, err)

	e, ok := matchFile(index, subsonicSong{Suffix: "mp3", Size: 5})
	require.True(t, ok)
	assert.Equal(t, p, e.path)

	// Suffix matched case-insensitively.
	_, ok = matchFile(index, subsonicSong{Suffix: "MP3", Size: 5})
	assert.True(t, ok)

	// Wrong size or suffix → not found.
	_, ok = matchFile(index, subsonicSong{Suffix: "mp3", Size: 999})
	assert.False(t, ok)
	_, ok = matchFile(index, subsonicSong{Suffix: "flac", Size: 5})
	assert.False(t, ok)
}

func TestMatchFile_AmbiguousSizeReturnsNotFound(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.mp3"), []byte("12345"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "y.mp3"), []byte("54321"), 0o644))
	index, err := buildFileIndex(root)
	require.NoError(t, err)

	_, ok := matchFile(index, subsonicSong{Suffix: "mp3", Size: 5})
	assert.False(t, ok, "a size+suffix collision must be reported as not-found, never guessed")
}

func TestResolveMountPoint_OK(t *testing.T) {
	resetLibraryMock(t)
	host.LibraryMock.On("GetLibrary", int32(7)).
		Return(&host.Library{ID: 7, MountPoint: "/libraries/7", Path: "/music/lib"}, nil)

	mp, err := resolveMountPoint("7")
	require.NoError(t, err)
	assert.Equal(t, "/libraries/7", mp)
}

func TestResolveMountPoint_NonNumericID(t *testing.T) {
	// Fails before any host call, so no mock expectation is needed.
	_, err := resolveMountPoint("lib1")
	assert.Error(t, err)
}

func TestResolveMountPoint_EmptyMountPointIsError(t *testing.T) {
	resetLibraryMock(t)
	host.LibraryMock.On("GetLibrary", int32(9)).
		Return(&host.Library{ID: 9, MountPoint: ""}, nil)

	_, err := resolveMountPoint("9")
	assert.Error(t, err, "an empty mount point means filesystem access was not granted")
}
