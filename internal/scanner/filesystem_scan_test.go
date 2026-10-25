//go:build !tinygo

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

// A build starts with empty buckets, so merging only adds records; a record
// for a path already present (a directory re-read after a resume) replaces it.
func TestMergeBucketRecords_AddsAndReplacesByPath(t *testing.T) {
	existing := []FileRecord{
		{Path: "/music/a.mp3", Mtime: 10},
		{Path: "/music/b.mp3", Mtime: 20},
	}
	records := []FileRecord{
		{Path: "/music/b.mp3", Mtime: 25},
		{Path: "/music/c.mp3", Mtime: 30},
	}

	merged := mergeBucketRecords(existing, records)
	assert.Equal(t, []FileRecord{
		{Path: "/music/a.mp3", Mtime: 10},
		{Path: "/music/b.mp3", Mtime: 25},
		{Path: "/music/c.mp3", Mtime: 30},
	}, merged)
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

// TestScanLibraryChunk_ResumesInsideDirectory proves a build cut off by the
// deadline in the middle of a directory continues after the last indexed
// entry instead of skipping the rest of the directory.
func TestScanLibraryChunk_ResumesInsideDirectory(t *testing.T) {
	resetKVStoreMock(t)
	root := t.TempDir()
	for _, name := range []string{"a.mp3", "b.mp3", "c.mp3"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(name), 0o644))
	}

	// a.mp3 was indexed before the deadline; b and c are still to do.
	state := &ScanState{PendingDirs: []string{root}, ResumeAfter: "a.mp3"}
	saved := map[string]bool{}
	host.KVStoreMock.On("Get", mock.Anything).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", libraryScanStateKey("1"), mock.Anything).Return(nil)
	host.KVStoreMock.On("Set", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		var records []FileRecord
		if json.Unmarshal(args.Get(1).([]byte), &records) == nil {
			for _, r := range records {
				saved[filepath.Base(r.Path)] = true
			}
		}
	}).Return(nil)

	require.NoError(t, scanLibraryChunk("1", state, time.Now().Add(time.Minute)))
	assert.True(t, state.Complete)
	assert.Empty(t, state.ResumeAfter)
	assert.Equal(t, map[string]bool{"b.mp3": true, "c.mp3": true}, saved,
		"only the entries after ResumeAfter are indexed in this chunk")
}

// TestScanLibraryChunk_QueuesSubdirectoriesAndPopsFinishedDirs proves
// subdirectories are queued and indexed, and finished directories leave the
// queue.
func TestScanLibraryChunk_QueuesSubdirectoriesAndPopsFinishedDirs(t *testing.T) {
	resetKVStoreMock(t)
	root := t.TempDir()
	sub := filepath.Join(root, "Album")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "song.mp3"), []byte("12345"), 0o644))

	host.KVStoreMock.On("Get", mock.Anything).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", mock.Anything, mock.Anything).Return(nil)

	state := &ScanState{PendingDirs: []string{root}}
	require.NoError(t, scanLibraryChunk("1", state, time.Now().Add(time.Minute)))
	assert.True(t, state.Complete)
	assert.Empty(t, state.PendingDirs)
	host.KVStoreMock.AssertCalled(t, "Set", bucketKey("1", 5, "mp3"), mock.Anything)
}
