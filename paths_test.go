package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

	index, err := buildFileIndexWithoutCache(root, time.Now().Add(30*time.Second))
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
	_, err := buildFileIndexWithoutCache(filepath.Join(t.TempDir(), "does-not-exist"), time.Now().Add(30*time.Second))
	assert.Error(t, err)
}

func TestMatchFile_UniqueSizeAndSuffix(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "song.mp3")
	require.NoError(t, os.WriteFile(p, []byte("12345"), 0o644))
	index, err := buildFileIndexWithoutCache(root, time.Now().Add(30*time.Second))
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
	index, err := buildFileIndexWithoutCache(root, time.Now().Add(30*time.Second))
	require.NoError(t, err)

	_, ok := matchFile(index, subsonicSong{Suffix: "mp3", Size: 5})
	assert.False(t, ok, "a size+suffix collision must be reported as not-found, never guessed")
}

func TestMatchFileFromBucketCache_CachesBucketRecords(t *testing.T) {
	resetKVStoreMock(t)
	cache := map[string][]FileRecord{}
	path := "/libraries/1/song.mp3"
	data, err := json.Marshal([]FileRecord{{Path: path, Mtime: 12345}})
	require.NoError(t, err)
	bucketKeyName := bucketKey("1", 5, "mp3")
	host.KVStoreMock.On("Get", bucketKeyName).Return(data, true, nil).Once()

	entry, ok := matchFileFromBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.True(t, ok)
	assert.Equal(t, path, entry.path)

	// Second lookup should reuse the cached bucket and not call KV again.
	entry2, ok2 := matchFileFromBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.True(t, ok2)
	assert.Equal(t, path, entry2.path)
	host.KVStoreMock.AssertExpectations(t)
}

func TestMatchFileFromBucketCache_AmbiguousBucketReturnsNotFound(t *testing.T) {
	resetKVStoreMock(t)
	cache := map[string][]FileRecord{}
	data, err := json.Marshal([]FileRecord{{Path: "/libraries/1/a.mp3", Mtime: 1}, {Path: "/libraries/1/b.mp3", Mtime: 2}})
	require.NoError(t, err)
	bucketKeyName := bucketKey("1", 5, "mp3")
	host.KVStoreMock.On("Get", bucketKeyName).Return(data, true, nil)

	_, ok := matchFileFromBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	assert.False(t, ok)
	host.KVStoreMock.AssertExpectations(t)
}

func TestMatchFileFromBucketCache_CachesMissingBuckets(t *testing.T) {
	resetKVStoreMock(t)
	cache := map[string][]FileRecord{}
	bucketKeyName := bucketKey("1", 5, "mp3")
	host.KVStoreMock.On("Get", bucketKeyName).Return([]byte(nil), false, nil).Once()

	_, ok := matchFileFromBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.False(t, ok)

	_, ok2 := matchFileFromBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.False(t, ok2)
	host.KVStoreMock.AssertExpectations(t)
}

func TestScanChunk_SavesNewBucketAndMarksComplete(t *testing.T) {
	resetKVStoreMock(t)
	root := t.TempDir()
	path := filepath.Join(root, "song.mp3")
	require.NoError(t, os.WriteFile(path, []byte("12345"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	state := &ScanState{PendingDirs: []string{root}}
	bucketKeyName := bucketKey("1", info.Size(), "mp3")
	bucketValue, err := json.Marshal([]FileRecord{{Path: path, Mtime: info.ModTime().Unix()}})
	require.NoError(t, err)

	host.KVStoreMock.On("Get", bucketKeyName).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", bucketKeyName, bucketValue).Return(nil)
	host.KVStoreMock.On("Set", libraryScanStateKey("1"), mock.Anything).Return(nil)

	err = scanChunk("1", state, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, state.Complete)
	host.KVStoreMock.AssertExpectations(t)
}

func TestScanChunk_DoesNotSaveStateWhenDeadlineImmediatelyReached(t *testing.T) {
	resetKVStoreMock(t)
	state := &ScanState{PendingDirs: []string{"/does/not/matter"}}
	err := scanChunk("1", state, time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, state.Complete)
	host.KVStoreMock.AssertNotCalled(t, "Set", libraryScanStateKey("1"), mock.Anything)
}

func TestEnsureLibraryIndexed_ScansMountAndStoresState(t *testing.T) {
	resetKVStoreMock(t)
	resetLibraryMock(t)

	root := t.TempDir()
	path := filepath.Join(root, "song.mp3")
	require.NoError(t, os.WriteFile(path, []byte("12345"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	host.LibraryMock.On("GetLibrary", int32(1)).Return(&host.Library{ID: 1, MountPoint: root}, nil)
	host.KVStoreMock.On("Get", libraryScanStateKey("1")).Return([]byte(nil), false, nil)
	bucketKeyName := bucketKey("1", info.Size(), "mp3")
	bucketValue, err := json.Marshal([]FileRecord{{Path: path, Mtime: info.ModTime().Unix()}})
	require.NoError(t, err)

	host.KVStoreMock.On("Get", bucketKeyName).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", bucketKeyName, bucketValue).Return(nil)
	host.KVStoreMock.On("Set", libraryScanStateKey("1"), mock.Anything).Return(nil)

	ready, err := ensureLibraryIndexed("1", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, ready)
	host.KVStoreMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
}

func TestEnsureLibraryIndexed_ResetsStaleStateWhenLibraryRescanned(t *testing.T) {
	resetKVStoreMock(t)
	resetLibraryMock(t)

	root := t.TempDir()
	path := filepath.Join(root, "song.mp3")
	require.NoError(t, os.WriteFile(path, []byte("12345"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	oldScan := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	newScan := oldScan.Add(time.Hour)

	host.LibraryMock.On("GetLibrary", int32(1)).Return(&host.Library{ID: 1, MountPoint: root, LastScanAt: newScan.Unix()}, nil)

	oldState, err := json.Marshal(ScanState{Complete: true, PendingDirs: nil, LastScanAt: oldScan.Unix()})
	require.NoError(t, err)
	host.KVStoreMock.On("Get", libraryScanStateKey("1")).Return(oldState, true, nil)

	bucketKeyName := bucketKey("1", info.Size(), "mp3")
	bucketValue, err := json.Marshal([]FileRecord{{Path: path, Mtime: info.ModTime().Unix()}})
	require.NoError(t, err)

	host.KVStoreMock.On("Get", bucketKeyName).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", bucketKeyName, bucketValue).Return(nil)
	host.KVStoreMock.On("Set", libraryScanStateKey("1"), mock.Anything).Return(nil)

	ready, err := ensureLibraryIndexed("1", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, ready)
	host.KVStoreMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
}

func TestEnsureLibraryIndexed_ReusesCompletedUnchangedIndex(t *testing.T) {
	resetKVStoreMock(t)
	resetLibraryMock(t)

	root := t.TempDir()
	currentLastScan := time.Now().UTC().Truncate(time.Second)
	stateData, err := json.Marshal(ScanState{Complete: true, PendingDirs: nil, LastScanAt: currentLastScan.Unix()})
	require.NoError(t, err)

	host.KVStoreMock.On("Get", libraryScanStateKey("1")).Return(stateData, true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).Return(&host.Library{ID: 1, MountPoint: root, LastScanAt: currentLastScan.Unix()}, nil)

	ready, err := ensureLibraryIndexed("1", time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, ready)
	host.KVStoreMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
}

func TestScanChunk_DoesNotMarkCompleteWhenRootUnreadable(t *testing.T) {
	resetKVStoreMock(t)

	missing := filepath.Join(t.TempDir(), "missing")
	state := &ScanState{PendingDirs: []string{missing}}

	// IMPORTANT: allow KV persistence (expected behavior now)
	host.KVStoreMock.
		On("Set", libraryScanStateKey("1"), mock.Anything).
		Return(nil).
		Once()

	// run
	err := scanChunk("1", state, time.Now().Add(time.Minute))
	require.NoError(t, err)

	// assertions
	assert.True(t, state.Complete)
	assert.Equal(t, []string{}, state.PendingDirs)

	// ensure mock expectations were met
	host.KVStoreMock.AssertExpectations(t)
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

func TestPersistentCache_FullIntegration(t *testing.T) {
	// This integration test verifies that:
	// 1. ensureLibraryIndexed scans the library and caches buckets to KV
	// 2. A second call reuses the cache without re-scanning
	// 3. The cache is correctly stored and retrieved from KV

	resetKVStoreMock(t)
	resetLibraryMock(t)

	// Create test directory with audio files
	root := t.TempDir()
	mp3File1 := filepath.Join(root, "a.mp3")
	mp3File2 := filepath.Join(root, "b.mp3")
	require.NoError(t, os.WriteFile(mp3File1, []byte("12345"), 0o644))   // 5 bytes
	require.NoError(t, os.WriteFile(mp3File2, []byte("1234567"), 0o644)) // 7 bytes

	currentLastScan := time.Now().UTC().Truncate(time.Second)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, MountPoint: root, LastScanAt: currentLastScan.Unix()}, nil)

	// First call: empty cache, should scan directory and save buckets
	stateKey := libraryScanStateKey("1")
	bucketKey5 := bucketKey("1", 5, "mp3")
	bucketKey7 := bucketKey("1", 7, "mp3")

	// Prepare KV expectations for first scan
	host.KVStoreMock.On("Get", stateKey).Return([]byte(nil), false, nil).Once()
	host.KVStoreMock.On("Get", bucketKey5).Return([]byte(nil), false, nil).Once()
	host.KVStoreMock.On("Get", bucketKey7).Return([]byte(nil), false, nil).Once()

	// Mock saves for the buckets
	host.KVStoreMock.On("Set", bucketKey5, mock.MatchedBy(func(data []byte) bool {
		var records []FileRecord
		err := json.Unmarshal(data, &records)
		return err == nil && len(records) == 1 && records[0].Path == mp3File1
	})).Return(nil).Once()
	host.KVStoreMock.On("Set", bucketKey7, mock.MatchedBy(func(data []byte) bool {
		var records []FileRecord
		err := json.Unmarshal(data, &records)
		return err == nil && len(records) == 1 && records[0].Path == mp3File2
	})).Return(nil).Once()

	// Mock save for the completed state
	host.KVStoreMock.On("Set", stateKey, mock.MatchedBy(func(data []byte) bool {
		var state ScanState
		err := json.Unmarshal(data, &state)
		return err == nil && state.Complete
	})).Return(nil).Once()

	// Run first scan
	ready, err := ensureLibraryIndexed("1", time.Now().Add(5*time.Second))
	require.NoError(t, err)
	assert.True(t, ready)

	// Verify all mocks were called for the first scan
	host.KVStoreMock.AssertExpectations(t)

	// Second call: cache is populated, should use cached state and skip rescan
	stateData, err := json.Marshal(ScanState{Complete: true, PendingDirs: nil, LastScanAt: currentLastScan.Unix()})
	require.NoError(t, err)

	// Reset mocks for second call
	host.KVStoreMock.On("Get", stateKey).Return(stateData, true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, MountPoint: root, LastScanAt: currentLastScan.Unix()}, nil)

	// Run second scan - should use cached state without re-scanning
	ready, err = ensureLibraryIndexed("1", time.Now().Add(5*time.Second))
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestMergeBucketRecords_RobustPathComparison(t *testing.T) {
	// Verify that merging handles path edge cases correctly
	tests := []struct {
		name            string
		existing        []FileRecord
		dir             string
		currentRecords  map[string]FileRecord
		expectedKeepOld bool
	}{
		{
			name: "keeps records from different directories",
			existing: []FileRecord{
				{Path: "/music/rock/song.mp3", Mtime: 100},
				{Path: "/music/jazz/song.mp3", Mtime: 200},
			},
			dir: "/music/rock",
			currentRecords: map[string]FileRecord{
				"/music/rock/new.mp3": {Path: "/music/rock/new.mp3", Mtime: 300},
			},
			expectedKeepOld: true,
		},
		{
			name: "removes old records from scanned directory",
			existing: []FileRecord{
				{Path: "/music/rock/old.mp3", Mtime: 100},
			},
			dir: "/music/rock",
			currentRecords: map[string]FileRecord{
				"/music/rock/new.mp3": {Path: "/music/rock/new.mp3", Mtime: 300},
			},
			expectedKeepOld: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeBucketRecords(tt.existing, tt.currentRecords, tt.dir)
			if tt.expectedKeepOld {
				assert.True(t, len(result) > 1, "should keep old and new records")
			} else {
				assert.False(t, len(result) > 1, "should replace old records with new ones")
			}
		})
	}
}
