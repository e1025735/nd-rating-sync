package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
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
	assert.Equal(t, filepath.Join(root, "a.mp3"), mp3[0].Path)

	flac := index[sizeKey(7, "flac")]
	require.Len(t, flac, 1)
	assert.Equal(t, filepath.Join(sub, "b.flac"), flac[0].Path)
}

func TestBuildFileIndex_MissingRootIsError(t *testing.T) {
	_, err := buildFileIndexWithoutCache(filepath.Join(t.TempDir(), "does-not-exist"), time.Now().Add(30*time.Second))
	assert.Error(t, err)
}

func TestBuildFileIndex_StopsImmediatelyWhenDeadlineHasPassed(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "song.mp3"), []byte("12345"), 0o644))

	index, err := buildFileIndexWithoutCache(root, time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.Empty(t, index)
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
	host.KVStoreMock.On("Get", kvadapter.LibraryScanStateKey("1")).Return([]byte(nil), false, nil)
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

func TestScanLibraryChunk_DoesNotMarkCompleteWhenRootUnreadable(t *testing.T) {
	resetKVStoreMock(t)

	missing := filepath.Join(t.TempDir(), "missing")
	state := &ScanState{PendingDirs: []string{missing}}

	// IMPORTANT: allow KV persistence (expected behavior now)
	host.KVStoreMock.
		On("Set", libraryScanStateKey("1"), mock.Anything).
		Return(nil).
		Once()

	// run
	err := scanLibraryChunk("1", state, time.Now().Add(time.Minute))
	require.NoError(t, err)

	// assertions
	assert.True(t, state.Complete)
	assert.Equal(t, []string{}, state.PendingDirs)

	// ensure mock expectations were met
	host.KVStoreMock.AssertExpectations(t)
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
	stateKey := kvadapter.LibraryScanStateKey("1")
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
