package scanner

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

func TestMergeBucketRecords_RemovesStaleEntriesFromTheCurrentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "library")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	external := filepath.Join(t.TempDir(), "other.mp3")
	require.NoError(t, os.WriteFile(external, []byte("data"), 0o644))

	currentPath := filepath.Join(dir, "current.mp3")
	oldPath := filepath.Join(dir, "old.mp3")
	newPath := filepath.Join(dir, "new.mp3")

	existing := []FileRecord{
		{Path: oldPath, Mtime: 10},
		{Path: currentPath, Mtime: 20},
		{Path: external, Mtime: 500},
	}
	currentRecords := map[string]FileRecord{
		currentPath: {Path: currentPath, Mtime: 25},
		newPath:     {Path: newPath, Mtime: 30},
	}

	merged := mergeBucketRecords(existing, currentRecords, dir)
	require.Len(t, merged, 3)

	result := map[string]int64{}
	for _, r := range merged {
		result[r.Path] = r.Mtime
	}
	assert.Equal(t, int64(25), result[currentPath])
	assert.Equal(t, int64(30), result[newPath])
	assert.Equal(t, int64(500), result[external])
	_, ok := result[oldPath]
	assert.False(t, ok, "stale entries from the scanned directory should be removed")
}

func TestBucketRecordsEqual_IgnoresOrderButRequiresMatchingPathsAndTimes(t *testing.T) {
	left := []FileRecord{{Path: "/music/a.mp3", Mtime: 100}, {Path: "/music/b.mp3", Mtime: 200}}
	right := []FileRecord{{Path: "/music/b.mp3", Mtime: 200}, {Path: "/music/a.mp3", Mtime: 100}}
	assert.True(t, bucketRecordsEqual(left, right))

	wrongMtime := []FileRecord{{Path: "/music/b.mp3", Mtime: 201}, {Path: "/music/a.mp3", Mtime: 100}}
	assert.False(t, bucketRecordsEqual(left, wrongMtime))

	duplicate := []FileRecord{{Path: "/music/a.mp3", Mtime: 100}, {Path: "/music/a.mp3", Mtime: 100}}
	assert.False(t, bucketRecordsEqual(left, duplicate))
}

func TestScanLibraryChunk_SavesNewBucketAndMarksComplete(t *testing.T) {
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

	err = scanLibraryChunk("1", state, time.Now().Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, state.Complete)
	host.KVStoreMock.AssertExpectations(t)
}

func TestScanLibraryChunk_DoesNotSaveStateWhenDeadlineImmediatelyReached(t *testing.T) {
	resetKVStoreMock(t)
	state := &ScanState{PendingDirs: []string{"/does/not/matter"}}
	err := scanLibraryChunk("1", state, time.Now().Add(-time.Second))
	require.NoError(t, err)
	assert.False(t, state.Complete)
	host.KVStoreMock.AssertNotCalled(t, "Set", libraryScanStateKey("1"), mock.Anything)
}
