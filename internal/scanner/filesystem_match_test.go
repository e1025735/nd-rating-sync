package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchFile_UniqueSizeAndSuffix(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "song.mp3")
	require.NoError(t, os.WriteFile(p, []byte("12345"), 0o644))
	index, err := buildFileIndexWithoutCache(root, time.Now().Add(30*time.Second))
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
	index, err := buildFileIndexWithoutCache(root, time.Now().Add(30*time.Second))
	require.NoError(t, err)

	_, ok := matchFile(index, subsonicSong{Suffix: "mp3", Size: 5})
	assert.False(t, ok, "a size+suffix collision must be reported as not-found, never guessed")
}

func TestMatchFileInBucketCache_CachesBucketRecords(t *testing.T) {
	resetKVStoreMock(t)
	cache := map[string][]FileRecord{}
	path := "/libraries/1/song.mp3"
	data, err := json.Marshal([]FileRecord{{Path: path, Mtime: 12345}})
	require.NoError(t, err)
	bucketKeyName := bucketKey("1", 5, "mp3")
	host.KVStoreMock.On("Get", bucketKeyName).Return(data, true, nil).Once()

	entry, ok := matchFileInBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.True(t, ok)
	assert.Equal(t, path, entry.Path)

	// Second lookup should reuse the cached bucket and not call KV again.
	entry2, ok2 := matchFileInBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.True(t, ok2)
	assert.Equal(t, path, entry2.Path)
	host.KVStoreMock.AssertExpectations(t)
}

func TestMatchFileInBucketCache_AmbiguousBucketReturnsNotFound(t *testing.T) {
	resetKVStoreMock(t)
	cache := map[string][]FileRecord{}
	data, err := json.Marshal([]FileRecord{{Path: "/libraries/1/a.mp3", Mtime: 1}, {Path: "/libraries/1/b.mp3", Mtime: 2}})
	require.NoError(t, err)
	bucketKeyName := bucketKey("1", 5, "mp3")
	host.KVStoreMock.On("Get", bucketKeyName).Return(data, true, nil)

	_, ok := matchFileInBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	assert.False(t, ok)
	host.KVStoreMock.AssertExpectations(t)
}

func TestMatchFileInBucketCache_CachesMissingBuckets(t *testing.T) {
	resetKVStoreMock(t)
	cache := map[string][]FileRecord{}
	bucketKeyName := bucketKey("1", 5, "mp3")
	host.KVStoreMock.On("Get", bucketKeyName).Return([]byte(nil), false, nil).Once()

	_, ok := matchFileInBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.False(t, ok)

	_, ok2 := matchFileInBucketCache("1", subsonicSong{ID: "s1", Size: 5, Suffix: "mp3"}, cache)
	require.False(t, ok2)
	host.KVStoreMock.AssertExpectations(t)
}
