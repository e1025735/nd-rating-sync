package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogem/id3v2/v2"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ─── extractStarsFromFile ─────────────────────────────────────────────────────

func TestExtractStarsFromFile_FileNotFound(t *testing.T) {
	stars, result := extractStarsFromFile("/no/such/file.mp3", "mp3", defaultTagOrder)
	assert.Equal(t, fileUnreadable, result)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_EmptyWAVIsUnreadable(t *testing.T) {
	// An empty .wav has no RIFF magic, so the parser cannot extract anything.
	// Must surface as fileUnreadable (not tagAbsent) so clear_rating_if_untagged
	// will not mistakenly clear the user's rating.
	f, err := os.CreateTemp(t.TempDir(), "song*.wav")
	require.NoError(t, err)
	f.Close()

	stars, result := extractStarsFromFile(f.Name(), "wav", defaultTagOrder)
	assert.Equal(t, tagAbsent, result, "empty WAV currently parses as 'no tag found' — this is fine; the read itself succeeded")
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_FLACWithFMPS(t *testing.T) {
	data := makeFLAC(t, "FMPS_RATING=0.6")
	path := filepath.Join(t.TempDir(), "song.flac")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	stars, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

func TestExtractStarsFromFile_OggVorbisWithFMPS(t *testing.T) {
	commentPkt := makeVorbisCommentPacket(t, "FMPS_RATING=0.8")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
	path := filepath.Join(t.TempDir(), "song.ogg")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	stars, result := extractStarsFromFile(path, "ogg", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}

func TestExtractStarsFromFile_OpusWithFoobar(t *testing.T) {
	commentPkt := makeOpusCommentPacket(t, "RATING=2")
	data := makeOggSinglePage(t, idHeaderPlaceholder, commentPkt)
	path := filepath.Join(t.TempDir(), "song.opus")
	require.NoError(t, os.WriteFile(path, data, 0o644))

	stars, result := extractStarsFromFile(path, "opus", []string{"foobar2000"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 2, stars)
}

func TestExtractStarsFromFile_UnsupportedExtensionFromPath(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "song*.aiff")
	require.NoError(t, err)
	f.Close()

	_, result := extractStarsFromFile(f.Name(), "", defaultTagOrder)
	assert.Equal(t, fileUnreadable, result, "aiff is unsupported and must surface as unreadable so clear_rating_if_untagged does not wipe the rating")
}

func TestExtractStarsFromFile_MP3NoRatingTag(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.mp3")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, result := extractStarsFromFile(path, "mp3", defaultTagOrder)
	assert.Equal(t, tagAbsent, result)
	assert.Equal(t, 0, stars)
}

func TestExtractStarsFromFile_MP3WithFMPS(t *testing.T) {
	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	stars, result := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

func TestExtractStarsFromFile_SuffixOverridesPathExtension(t *testing.T) {
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: "0.8",
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.bin")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))

	stars, result := extractStarsFromFile(path, "mp3", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 4, stars)
}

// ─── runSyncStep ─────────────────────────────────────────────────────────────

func TestRunSyncStep_NoLibraries(t *testing.T) {
	err := runSyncStep(pluginConfig{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

// ─── KV-backed file index cache (integration) ────────────────────────────────

// TestRunSyncChunk_UsesCachedFileIndex proves the KV-backed file index cache
// is actually used end-to-end: we deliberately point the library's MountPoint
// at an EMPTY temp dir so a fresh walk would find nothing, but seed KV with
// a cached blob whose entry points at a real tagged file elsewhere. The
// pair only succeeds if cachedFileIndex prefers the KV blob over rebuilding.
func TestRunSyncChunk_UsesCachedFileIndex(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	// Real tagged file outside the empty mount.
	fileDir := t.TempDir()
	tagged := writeFMPSFileAt(t, fileDir, "song.mp3", "0.8") // 4 stars
	taggedSize := fileSize(t, tagged)

	// Empty mount + non-zero LastScanAt so the cache validity check passes.
	emptyMount := t.TempDir()
	lastScan := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	mockGetLibraryWithScan(1, emptyMount, lastScan.Unix())

	// Seed KV with a cached blob whose only entry points at the real file.
	blob := cachedFileIndexBlob{
		Version:    fileIndexCacheVersion,
		LastScanAt: lastScan.Unix(),
		Entries:    []fileEntry{{Path: tagged, Size: taggedSize, Mtime: time.Unix(1000, 0).UTC()}},
	}
	raw, err := json.Marshal(blob)
	require.NoError(t, err)
	host.KVStoreMock.On("Get", "file-index:1").Return(raw, true, nil)
	// No threshold and incremental off → KV last-synced not touched.

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "song-1", Title: "T", Suffix: "mp3", Size: taggedSize}}), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=4&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
}

// TestRunSyncChunk_StaleCacheRebuildsAndOverwrites proves that when KV holds a
// cached index stamped at an OLDER LastScanAt, the next chunk discards it,
// walks the mount fresh, and overwrites the cache with the new stamp.
func TestRunSyncChunk_StaleCacheRebuildsAndOverwrites(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6") // 3 stars
	size := fileSize(t, path)

	currentScan := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	mockGetLibraryWithScan(1, mount, currentScan.Unix())

	// Stale cache: bogus path + older LastScanAt. Validation must reject it.
	staleBlob := cachedFileIndexBlob{
		Version:    fileIndexCacheVersion,
		LastScanAt: currentScan.Add(-time.Hour).Unix(),
		Entries:    []fileEntry{{Path: "/does/not/exist.mp3", Size: 9999, Mtime: time.Unix(1, 0)}},
	}
	raw, err := json.Marshal(staleBlob)
	require.NoError(t, err)
	host.KVStoreMock.On("Get", "file-index:1").Return(raw, true, nil)
	// After the fresh walk we expect a Set with the CURRENT LastScanAt.
	var saved []byte
	host.KVStoreMock.On("Set", "file-index:1", mock.Anything).
		Run(func(args mock.Arguments) { saved = append([]byte(nil), args.Get(1).([]byte)...) }).
		Return(nil)

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "song-1", Title: "T", Suffix: "mp3", Size: size}}), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}
	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))

	host.SubsonicAPIMock.AssertExpectations(t)
	require.NotNil(t, saved, "fresh walk after stale cache must persist a new blob")

	var got cachedFileIndexBlob
	require.NoError(t, json.Unmarshal(saved, &got))
	assert.Equal(t, currentScan.Unix(), got.LastScanAt, "rewritten blob carries the CURRENT LastScanAt")
}

// mockGetLibraryWithScan is the LastScanAt-aware variant of mockGetLibrary;
// the cache validation path needs a non-zero scan stamp to engage.
func mockGetLibraryWithScan(libID int32, mountPoint string, lastScanUnix int64) {
	host.LibraryMock.On("GetLibrary", libID).
		Return(&host.Library{ID: libID, MountPoint: mountPoint, Path: mountPoint, LastScanAt: lastScanUnix}, nil)
}

// ─── Per-pair sync (integration) ─────────────────────────────────────────────

// TestRunSyncForUser_HappyPath wires the full pipeline together: a real
// ID3-tagged file sits under the library mount, SubsonicAPI returns one song
// whose size matches that file, the scanner locates it by size, reads the
// rating, and setRating is called with the correct star count.
func TestSyncPair_HappyPath(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)

	// 1. Real ID3 file with FMPS_Rating=0.6 (→ 3 stars).
	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song-1.mp3", "0.6")
	mockGetLibrary(1, mount)

	// 2. fetchAllSongs returns one song whose size matches that file. Path is
	// deliberately omitted: the production code never reads it (it would be a
	// synthesized fake from Navidrome), only size+suffix.
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)

	// 3. setRating expects rating=3.
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users: []userConfig{{
			Username:         "alice",
			SkipAlreadyRated: true,
			RatingTagOrder:   []string{"MediaMonkey"},
		}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
}

// ─── Regression: read failures must not trigger clear ────────────────────────

// TestSyncPair_UnreadableFileWithClear_DoesNotClearRating proves that a
// song the plugin cannot locate on disk is NOT treated as "no tag found" when
// clear_rating_if_untagged=true. Conflating the two would wipe the user's
// existing Navidrome rating whenever a file can't be matched.
func TestSyncPair_UnreadableFileWithClear_DoesNotClearRating(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)

	// Empty mount: the song's size matches no file, so matchFile reports
	// not-found and the scanner must surface this as fileUnreadable.
	mount := t.TempDir()
	mockGetLibrary(1, mount)

	// Song points at a path that doesn't exist — extractStarsFromFile must
	// surface this as fileUnreadable.
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: 4242}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	// CRITICAL: setRating must NOT be called with rating=0. The mock has no
	// expectation registered for it, and AssertNotCalled below makes the
	// invariant explicit.

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users: []userConfig{{
			Username:              "alice",
			ClearRatingIfUntagged: true,
			RatingTagOrder:        []string{"MediaMonkey"},
		}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=0&u=alice")
}

// ─── Incremental sync ─────────────────────────────────────────────────────────

func TestSyncPair_IncrementalFirstRun_ProcessesAllAndSavesThreshold(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockGetLibrary(1, mount)
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	// First run: KV miss → full scan.
	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(nil), false, nil).Once()
	// At end of run, scan-start timestamp is written back.
	host.KVStoreMock.On("Set", "last-synced:1:alice", mock.Anything).
		Return(nil).Once()

	cfg := pluginConfig{IncrementalSync: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

func TestSyncPair_IncrementalSkipsUnchangedFile(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockGetLibrary(1, mount)
	// Make the file appear older than the threshold by setting mtime to
	// a fixed past instant.
	fileTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, fileTime, fileTime))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	// setRating should NOT be called — the file is unchanged.

	threshold := fileTime.Add(time.Hour) // strictly after the file's mtime
	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil).Once()
	host.KVStoreMock.On("Set", "last-synced:1:alice", mock.Anything).
		Return(nil).Once()

	cfg := pluginConfig{IncrementalSync: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=3&u=alice")
	host.KVStoreMock.AssertExpectations(t)
}

func TestSyncPair_IncrementalProcessesNewerFile(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.8") // 4 stars
	mockGetLibrary(1, mount)
	// File mtime in the future of the threshold → must be processed.
	fileTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, fileTime, fileTime))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=4&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	threshold := fileTime.Add(-time.Hour) // before file mtime
	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil).Once()
	host.KVStoreMock.On("Set", "last-synced:1:alice", mock.Anything).
		Return(nil).Once()

	cfg := pluginConfig{IncrementalSync: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

func TestSyncPair_IncrementalDisabled_BypassesKV(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockGetLibrary(1, mount)
	// File mtime far in the past — would be skipped if incremental were on.
	require.NoError(t, os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	cfg := pluginConfig{IncrementalSync: false, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	// Confirm KV is never touched when incremental is off.
	host.KVStoreMock.AssertNotCalled(t, "Get", mock.Anything)
	host.KVStoreMock.AssertNotCalled(t, "Set", mock.Anything, mock.Anything)
}

// ─── Chunked / resumable sync ─────────────────────────────────────────────────

// TestProcessPairChunk_StopsAtDeadlineMidPair proves the time budget yields
// within a bounded number of songs after the deadline passes, and returns an
// advanced cursor so the next call resumes where this one stopped. Yield
// granularity is deadlineCheckEvery: we tolerate processing up to that many
// songs past the deadline before yielding, so the test feeds 2× that many
// already-rated songs.
//
// Calls processPairChunk directly so the file index is the caller's
// responsibility; we pass nil because every song is skipped on
// SkipAlreadyRated before matchFile is consulted.
func TestProcessPairChunk_StopsAtDeadlineMidPair(t *testing.T) {
	resetSubsonicMock(t)

	songs := make([]subsonicSong, 2*deadlineCheckEvery)
	for i := range songs {
		songs[i] = subsonicSong{ID: fmt.Sprintf("%d", i+1), Title: "S", UserRating: 5}
	}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}

	deadline := time.Now().Add(-time.Second) // already past
	next, pairDone := processPairChunk(lib, user, pluginConfig{}, syncCursor{}, time.Time{}, deadline, nil)

	assert.False(t, pairDone, "deadline hit mid-pair → pair not done")
	assert.Equal(t, deadlineCheckEvery, next.Offset,
		"yields exactly at the deadline-check boundary (deadlineCheckEvery songs)")
	host.SubsonicAPIMock.AssertExpectations(t)
}

// TestRunSyncChunk_AdvancesAcrossPairsToCompletion proves a generous deadline
// walks every (library, user) pair and reports the sweep complete.
//
// The library is mocked with a real temp-dir mount so cachedFileIndex can
// resolve and walk it; both songs are skip-rated, so the mount is empty.
func TestRunSyncChunk_AdvancesAcrossPairsToCompletion(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	mockGetLibrary(1, t.TempDir())

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=bob&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "b1", UserRating: 5}}), nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users: []userConfig{
			{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
			{Username: "bob", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
		},
	}}}

	next, done := runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done, "both pairs processed → sweep complete")
	assert.Equal(t, 1, next.Lib, "cursor advanced past the only library")
	host.SubsonicAPIMock.AssertExpectations(t)
}

// TestRunSyncStepUntil_ReschedulesWhenBudgetExceeded proves an unfinished sweep
// persists its cursor into a fresh one-time callback (empty scheduleID → host
// mints a unique one).
func TestRunSyncStepUntil_ReschedulesWhenBudgetExceeded(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "lib1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	// Fresh full sweep: no heartbeat present → proceeds and records one.
	host.KVStoreMock.On("Get", "sweep-active").Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", "sweep-active", mock.Anything).Return(nil)

	host.SchedulerMock.On("ScheduleOneTime", int32(0), `{"lib":0,"user":0,"off":0,"start":""}`, "").
		Return("cont-id", nil)

	err := runSyncStepUntil(cfg, "", time.Now().Add(-time.Second))
	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
	assert.Empty(t, host.SubsonicAPIMock.Calls, "no song work happens once the budget is already gone")
}

// TestRunSyncStepUntil_NoRescheduleWhenComplete proves a sweep that finishes
// inside the budget does not schedule a continuation.
func TestRunSyncStepUntil_NoRescheduleWhenComplete(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	mockGetLibrary(1, t.TempDir())

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)

	// Fresh full sweep that finishes inside the budget: records, then clears, the
	// in-progress heartbeat.
	host.KVStoreMock.On("Get", "sweep-active").Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", "sweep-active", mock.Anything).Return(nil)
	host.KVStoreMock.On("Delete", "sweep-active").Return(nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	err := runSyncStepUntil(cfg, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, host.SchedulerMock.Calls, "a completed sweep schedules no continuation")
	host.KVStoreMock.AssertExpectations(t)
}

// ─── LastScanAt gate ──────────────────────────────────────────────────────────

// TestRunSyncChunk_GateSkipsUnchangedLibrary proves the gate skips ALL song
// paging for a pair whose library has not been rescanned since our last sweep,
// and does NOT advance the stored threshold (nothing was processed).
func TestRunSyncChunk_GateSkipsUnchangedLibrary(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lastScan := threshold.Add(-time.Hour) // Navidrome scanned BEFORE our last sweep

	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: lastScan.Unix()}, nil)

	cfg := pluginConfig{IncrementalSync: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	next, done := runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done, "single unchanged pair → sweep complete")
	assert.Equal(t, 1, next.Lib, "cursor advanced past the only library")
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
	host.KVStoreMock.AssertNotCalled(t, "Set", mock.Anything, mock.Anything)
	host.LibraryMock.AssertExpectations(t)
}

// TestRunSyncChunk_GateProcessesRescannedLibrary proves the gate opens (pages
// normally and re-saves the threshold) once LastScanAt advances past it.
//
// The Library mock returns a real MountPoint so cachedFileIndex can walk it
// after the gate opens (the gate uses the same GetLibrary call). The song is
// skip-rated, so the empty mount is fine.
func TestRunSyncChunk_GateProcessesRescannedLibrary(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lastScan := threshold.Add(time.Hour) // Navidrome rescanned AFTER our last sweep

	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: lastScan.Unix(), MountPoint: t.TempDir()}, nil)
	// Gate opens → file index cache miss → walk → persist. With LastScanAt
	// non-zero we now expect a load attempt and a write.
	host.KVStoreMock.On("Get", "file-index:1").Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", "file-index:1", mock.Anything).Return(nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)
	host.KVStoreMock.On("Set", "last-synced:1:alice", mock.Anything).Return(nil)

	cfg := pluginConfig{IncrementalSync: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	_, done := runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

// ─── In-progress guard ────────────────────────────────────────────────────────

// TestRunSyncStepUntil_SkipsWhenSweepInProgress proves a fresh full sweep bows
// out when another sweep's heartbeat is fresh.
func TestRunSyncStepUntil_SkipsWhenSweepInProgress(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	host.KVStoreMock.On("Get", "sweep-active").
		Return([]byte(time.Now().UTC().Format(time.RFC3339Nano)), true, nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	err := runSyncStepUntil(cfg, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	host.KVStoreMock.AssertNotCalled(t, "Set", mock.Anything, mock.Anything)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
	assert.Empty(t, host.SchedulerMock.Calls, "an in-progress sweep blocks a second one")
}

// TestRunSyncStepUntil_ProceedsWhenSweepStale proves a stale heartbeat (older
// than sweepStaleAfter) does not block a fresh sweep.
func TestRunSyncStepUntil_ProceedsWhenSweepStale(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	stale := time.Now().Add(-2 * sweepStaleAfter).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", "sweep-active").Return([]byte(stale), true, nil)
	host.KVStoreMock.On("Set", "sweep-active", mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), `{"lib":0,"user":0,"off":0,"start":""}`, "").
		Return("cont-id", nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	err := runSyncStepUntil(cfg, "", time.Now().Add(-time.Second))
	require.NoError(t, err)
	host.KVStoreMock.AssertExpectations(t)
	host.SchedulerMock.AssertExpectations(t)
}

// TestRunSyncStepUntil_ContinuationRefreshesGuardNotChecks proves a continuation
// (non-empty cursor) refreshes the heartbeat but never runs the in-progress
// check, so a long import's own continuations can never block themselves.
func TestRunSyncStepUntil_ContinuationRefreshesGuardNotChecks(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	host.KVStoreMock.On("Set", "sweep-active", mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), mock.Anything, "").Return("cont-id", nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	err := runSyncStepUntil(cfg, `{"lib":0,"user":0,"off":3,"start":"2026-06-01T00:00:00Z"}`, time.Now().Add(-time.Second))
	require.NoError(t, err)
	host.KVStoreMock.AssertNotCalled(t, "Get", "sweep-active")
	host.KVStoreMock.AssertExpectations(t)
	host.SchedulerMock.AssertExpectations(t)
}

// writeFMPSFile creates a temp .mp3 with an FMPS_Rating TXXX frame.
func writeFMPSFileAt(t *testing.T, dir, name, value string) string {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: value,
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}

// writeFMPSFile creates a temp .mp3 with an FMPS_Rating TXXX frame.
func writeFMPSFile(t *testing.T, value string) string {
	t.Helper()
	return writeFMPSFileAt(t, t.TempDir(), "song.mp3", value)
}

// fileSize returns the on-disk byte size of path — used to set the Subsonic
// `size` a test song reports, so matchFile can locate it under the mount.
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	return fi.Size()
}

// mockGetLibrary makes host.LibraryGetLibrary(libID) return a library mounted
// at mountPoint. Path == MountPoint here, which is all the size-match flow needs.
func mockGetLibrary(libID int32, mountPoint string) {
	host.LibraryMock.On("GetLibrary", libID).
		Return(&host.Library{ID: libID, MountPoint: mountPoint, Path: mountPoint}, nil)
}
