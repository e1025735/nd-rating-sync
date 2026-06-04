package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSizeKey_CombinesSizeAndExt(t *testing.T) {
	assert.Equal(t, "123:mp3", sizeKey(123, "mp3"))
	assert.NotEqual(t, sizeKey(123, "mp3"), sizeKey(123, "flac"))
}

func TestIsSupportedExt(t *testing.T) {
	for _, ext := range []string{"mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4"} {
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
	assert.Equal(t, filepath.Join(root, "a.mp3"), mp3[0].Path)

	flac := index[sizeKey(7, "flac")]
	require.Len(t, flac, 1)
	assert.Equal(t, filepath.Join(sub, "b.flac"), flac[0].Path)
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
	assert.Equal(t, p, e.Path)

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

// ─── KV-backed file index cache ───────────────────────────────────────────────

// TestSaveAndLoadCachedIndex_RoundTrip proves that an index saved by
// saveCachedIndex can be reloaded into an equivalent bucket map by
// loadCachedIndex when the LastScanAt stamp matches.
func TestSaveAndLoadCachedIndex_RoundTrip(t *testing.T) {
	resetKVStoreMock(t)

	lastScan := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	want := map[string][]fileEntry{
		sizeKey(5, "mp3"):  {{Path: "/m/a.mp3", Size: 5, Mtime: time.Unix(1000, 0).UTC()}},
		sizeKey(7, "flac"): {{Path: "/m/Artist/Album/b.flac", Size: 7, Mtime: time.Unix(2000, 0).UTC()}},
	}

	// Capture whatever saveCachedIndex writes, then wire it back to Get for
	// the load. testify's Return values evaluate at setup time, so we set up
	// Get only after Set has populated `stored`.
	var stored []byte
	host.KVStoreMock.On("Set", "file-index:42", mock.Anything).
		Run(func(args mock.Arguments) { stored = append([]byte(nil), args.Get(1).([]byte)...) }).
		Return(nil).Once()

	saveCachedIndex("42", lastScan, want)
	require.NotNil(t, stored, "saveCachedIndex must persist")

	host.KVStoreMock.On("Get", "file-index:42").Return(stored, true, nil).Once()
	got, ok := loadCachedIndex("42", lastScan)
	require.True(t, ok, "round-trip with matching LastScanAt must hit")

	// Two buckets in, two buckets out, with the same single entry each.
	require.Len(t, got, 2)
	mp3 := got[sizeKey(5, "mp3")]
	require.Len(t, mp3, 1)
	assert.Equal(t, "/m/a.mp3", mp3[0].Path)
	assert.Equal(t, int64(5), mp3[0].Size)
	assert.True(t, time.Unix(1000, 0).UTC().Equal(mp3[0].Mtime))

	flac := got[sizeKey(7, "flac")]
	require.Len(t, flac, 1)
	assert.Equal(t, "/m/Artist/Album/b.flac", flac[0].Path)
}

func TestLoadCachedIndex_StaleLastScanAtMisses(t *testing.T) {
	resetKVStoreMock(t)

	// Cache stamped at t=1000; lookup expects t=2000. Validation rejects it.
	blob := cachedFileIndexBlob{
		Version:    fileIndexCacheVersion,
		LastScanAt: 1000,
		Entries:    []fileEntry{{Path: "/m/a.mp3", Size: 5, Mtime: time.Unix(500, 0).UTC()}},
	}
	raw, err := json.Marshal(blob)
	require.NoError(t, err)
	host.KVStoreMock.On("Get", "file-index:1").Return(raw, true, nil).Once()

	_, ok := loadCachedIndex("1", time.Unix(2000, 0).UTC())
	assert.False(t, ok, "stale LastScanAt → caller must rebuild")
}

func TestLoadCachedIndex_WrongVersionMisses(t *testing.T) {
	resetKVStoreMock(t)

	// Future schema version: treated as a miss so we never feed mismatched
	// data into the rest of the pipeline.
	blob := cachedFileIndexBlob{
		Version:    fileIndexCacheVersion + 1,
		LastScanAt: 1000,
		Entries:    []fileEntry{{Path: "/m/a.mp3", Size: 5, Mtime: time.Unix(500, 0).UTC()}},
	}
	raw, err := json.Marshal(blob)
	require.NoError(t, err)
	host.KVStoreMock.On("Get", "file-index:1").Return(raw, true, nil).Once()

	_, ok := loadCachedIndex("1", time.Unix(1000, 0).UTC())
	assert.False(t, ok, "version mismatch → caller must rebuild")
}

func TestLoadCachedIndex_MalformedMisses(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", "file-index:1").Return([]byte("not json"), true, nil).Once()

	_, ok := loadCachedIndex("1", time.Unix(1000, 0).UTC())
	assert.False(t, ok, "malformed cache must fail-open, not error out")
}

func TestLoadCachedIndex_KVErrorMisses(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", "file-index:1").
		Return([]byte(nil), false, errors.New("kv unavailable")).Once()

	_, ok := loadCachedIndex("1", time.Unix(1000, 0).UTC())
	assert.False(t, ok, "KV failure must fail-open")
}

func TestSaveCachedIndex_OversizedNotPersisted(t *testing.T) {
	resetKVStoreMock(t)
	// The mock has no Set expectation — calling it would fail the test.

	// Build an index whose serialised form exceeds maxIndexBytes. A path of
	// ~80 bytes × ~80k entries is well over 4 MiB.
	bigPath := strings.Repeat("a", 80)
	idx := map[string][]fileEntry{}
	for i := 0; i < 80000; i++ {
		key := sizeKey(int64(i), "mp3")
		idx[key] = append(idx[key], fileEntry{Path: bigPath, Size: int64(i), Mtime: time.Unix(int64(i), 0).UTC()})
	}

	saveCachedIndex("big", time.Unix(1000, 0).UTC(), idx)
	host.KVStoreMock.AssertNotCalled(t, "Set", "file-index:big", mock.Anything)
}

func TestKVKeyFileIndex_EscapesLibraryID(t *testing.T) {
	// A future non-numeric ID with a ':' must not collide with another key.
	assert.Equal(t, "file-index:a%3Ab", kvKeyFileIndex("a:b"))
	assert.Equal(t, "file-index:1", kvKeyFileIndex("1"))
}
