//go:build !tinygo

package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ─── runSyncStep ─────────────────────────────────────────────────────────────

func TestRunSyncStep_NoLibraries(t *testing.T) {
	allowSweepRecordWrites()
	err := RunSyncStep(PluginConfig{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

// ─── Regression: read failures must not trigger clear ────────────────────────

// TestSyncPair_UnreadableFileWithClear_DoesNotClearRating proves that a
// song the plugin cannot locate on disk is NOT treated as "no tag found" when
// clear_rating_if_untagged=true. Conflating the two would wipe the user's
// existing Navidrome rating whenever a file can't be matched.
func TestSyncPair_UnreadableFileWithClear_DoesNotClearRating(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()

	resetMatcherMock(t)

	// The matcher finds no track for the song, so its file cannot be located
	// and the scanner must surface this as FileUnreadable.
	mockGetLibrary(1, t.TempDir())
	mockMatch([]string{"song-1"}, nil)

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: 4242}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	// CRITICAL: setRating must NOT be called with rating=0. The mock has no
	// expectation registered for it, and AssertNotCalled below makes the
	// invariant explicit.

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users: []UserConfig{{
			Username:              "alice",
			ClearRatingIfUntagged: true,
			RatingTagOrder:        []string{"MediaMonkey"},
		}},
	}}}

	runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=0&u=alice")
}

// ─── Incremental sync ─────────────────────────────────────────────────────────

func TestSyncPair_IncrementalFirstRun_ProcessesAllAndSavesThreshold(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockGetLibrary(1, mount)
	mockMatch([]string{"song-1"}, trackAt("song-1", 1, "song.mp3"))
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	// First run: KV miss → full scan.
	host.KVStoreMock.On("Get", kvKeyLastSynced("1", "alice")).
		Return([]byte(nil), false, nil).Once()
	// At end of run, scan-start timestamp is written back.
	host.KVStoreMock.On("Set", kvKeyLastSynced("1", "alice"), mock.Anything).
		Return(nil).Once()

	cfg := PluginConfig{IncrementalSync: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

func TestSyncPair_IncrementalSkipsUnchangedFile(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockGetLibrary(1, mount)
	mockMatch([]string{"song-1"}, trackAt("song-1", 1, "song.mp3"))
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
	host.KVStoreMock.On("Get", kvKeyLastSynced("1", "alice")).
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil).Once()
	host.KVStoreMock.On("Set", kvKeyLastSynced("1", "alice"), mock.Anything).
		Return(nil).Once()

	cfg := PluginConfig{IncrementalSync: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=3&u=alice")
	host.KVStoreMock.AssertExpectations(t)
}

func TestSyncPair_IncrementalProcessesNewerFile(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()
	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.8") // 4 stars
	mockGetLibrary(1, mount)
	mockMatch([]string{"song-1"}, trackAt("song-1", 1, "song.mp3"))
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
	host.KVStoreMock.On("Get", kvKeyLastSynced("1", "alice")).
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil).Once()
	host.KVStoreMock.On("Set", kvKeyLastSynced("1", "alice"), mock.Anything).
		Return(nil).Once()

	cfg := PluginConfig{IncrementalSync: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

func TestSyncPair_IncrementalDisabled_BypassesKV(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockGetLibrary(1, mount)
	mockMatch([]string{"song-1"}, trackAt("song-1", 1, "song.mp3"))
	// File mtime far in the past — would be skipped if incremental were on.
	require.NoError(t, os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	cfg := PluginConfig{IncrementalSync: false, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	host.SubsonicAPIMock.AssertExpectations(t)
	// Confirm no incremental threshold is read or written when incremental is off.
	host.KVStoreMock.AssertNotCalled(t, "Get", mock.Anything)
	host.KVStoreMock.AssertNotCalled(t, "Set", kvKeyLastSynced("1", "alice"), mock.Anything)
}

// ─── Chunked / resumable sync ─────────────────────────────────────────────────

// TestProcessPairChunk_StopsAtDeadlineMidPair proves the time budget yields
// within a bounded number of songs after the deadline passes, and returns an
// advanced cursor so the next call resumes where this one stopped. Yield
// granularity is deadlineCheckEvery: we tolerate processing up to that many
// songs past the deadline before yielding, so the test feeds 2× that many
// already-rated songs.
//
// Calls processPairChunk directly with an empty mount: every song is skipped
// on SkipAlreadyRated, so no file is ever located.
func TestProcessPairChunk_StopsAtDeadlineMidPair(t *testing.T) {
	resetSubsonicMock(t)
	allowSweepRecordWrites()

	songs := make([]subsonicSong, 2*deadlineCheckEvery)
	for i := range songs {
		songs[i] = subsonicSong{ID: fmt.Sprintf("%d", i+1), Title: "S", UserRating: 5}
	}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)

	lib := LibraryConfig{LibraryID: "lib1"}
	user := UserConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}

	deadline := time.Now().Add(-time.Second) // already past
	next, pairDone := processPairChunk(lib, user, PluginConfig{}, SyncCursor{}, time.Time{}, deadline, libraryMount{})

	assert.False(t, pairDone, "deadline hit mid-pair → pair not done")
	assert.Equal(t, deadlineCheckEvery, next.Offset,
		"yields exactly at the deadline-check boundary (deadlineCheckEvery songs)")
	host.SubsonicAPIMock.AssertExpectations(t)
}

// TestRunSyncChunk_AdvancesAcrossPairsToCompletion proves a generous deadline
// walks every (library, user) pair and reports the sweep complete.
//
// Both songs are skip-rated, so no file is located and the mount is empty.
func TestRunSyncChunk_AdvancesAcrossPairsToCompletion(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()
	mockGetLibrary(1, t.TempDir())

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=bob&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "b1", UserRating: 5}}), nil)

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users: []UserConfig{
			{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder},
			{Username: "bob", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder},
		},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done, "both pairs processed → sweep complete")
	assert.Equal(t, 1, next.Lib, "cursor advanced past the only library")
	host.SubsonicAPIMock.AssertExpectations(t)
}

// TestRunSyncChunk_ReadsFileLocatedByMatcher proves the file is found by the
// matcher's library-relative path, also below subfolders and when another
// file has the same size (size matching would have called it ambiguous).
func TestRunSyncChunk_ReadsFileLocatedByMatcher(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()

	mount := t.TempDir()
	album := filepath.Join(mount, "Artist", "Album")
	require.NoError(t, os.MkdirAll(album, 0o755))
	path := writeFMPSFileAt(t, album, "01 - Song.mp3", "0.6")
	twin := writeFMPSFileAt(t, mount, "twin.mp3", "1.0")
	require.Equal(t, fileSize(t, path), fileSize(t, twin), "same size: only the path tells them apart")
	mockGetLibrary(1, mount)
	mockMatch([]string{"song-1"}, trackAt("song-1", 1, "Artist/Album/01 - Song.mp3"))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: fileSize(t, path)}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	require.True(t, done)
	assert.Equal(t, 1, next.Lib)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.MatcherMock.AssertExpectations(t)
}

// TestRunSyncChunk_OnlyUnratedSongsAreLocated proves songs skipped as already
// rated are not sent to the matcher.
func TestRunSyncChunk_OnlyUnratedSongsAreLocated(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()

	mockGetLibrary(1, t.TempDir())
	mockMatch([]string{"new"}, nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "rated", UserRating: 4}, {ID: "new"}}), nil)

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}

	_, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done)
	host.MatcherMock.AssertExpectations(t)
}

// TestRunSyncChunk_LibraryWithoutMountIsSkipped proves a library the plugin
// has no filesystem access to is skipped before any song is listed.
func TestRunSyncChunk_LibraryWithoutMountIsSkipped(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()
	host.LibraryMock.On("GetLibrary", int32(1)).Return(&host.Library{ID: 1, Path: hostLibraryRoot}, nil)

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", RatingTagOrder: DefaultTagOrder}},
	}}}

	_, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", mock.Anything)
}

// TestRunSyncStepUntil_ReschedulesWhenBudgetExceeded proves an unfinished sweep
// persists its cursor into a fresh one-time callback (empty scheduleID → host
// mints a unique one).
func TestRunSyncStepUntil_ReschedulesWhenBudgetExceeded(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	allowSweepRecordWrites()

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "lib1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}

	// Fresh full sweep: no heartbeat present → proceeds and records one.
	host.KVStoreMock.On("Get", kvKeySweepActive()).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)

	host.SchedulerMock.On("ScheduleOneTime", int32(0), `{"lib":0,"user":0,"off":0,"start":""}`, "").
		Return("cont-id", nil)

	err := runSyncStepUntil(cfg, "", time.Now().Add(-time.Second))
	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
	assert.Empty(t, host.SubsonicAPIMock.Calls, "no song work happens once the budget is already gone")
}

func TestRunSyncStepUntil_NoRescheduleWhenComplete(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()
	mockGetLibrary(1, t.TempDir())

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)

	// Fresh full sweep that finishes inside the budget: records, then clears, the
	// in-progress heartbeat.
	host.KVStoreMock.On("Get", kvKeySweepActive()).Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.KVStoreMock.On("Delete", kvKeySweepActive()).Return(nil)

	err := runSyncStepUntil(cfg, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, host.SchedulerMock.Calls, "a completed sweep schedules no continuation")
	host.KVStoreMock.AssertExpectations(t)
}

func TestRunSyncStepUntil_SkipsWhenSweepInProgress(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	allowSweepRecordWrites()

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}
	host.KVStoreMock.On("Get", kvKeySweepActive()).
		Return([]byte(time.Now().UTC().Format(time.RFC3339Nano)), true, nil)

	err := runSyncStepUntil(cfg, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	host.KVStoreMock.AssertNotCalled(t, "Set", mock.Anything, mock.Anything)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
	assert.Empty(t, host.SchedulerMock.Calls, "an in-progress sweep blocks a second one")
}

func TestRunSyncStepUntil_ProceedsWhenSweepStale(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	allowSweepRecordWrites()

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}

	stale := time.Now().Add(-2 * sweepStaleAfter).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", kvKeySweepActive()).Return([]byte(stale), true, nil)
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), `{"lib":0,"user":0,"off":0,"start":""}`, "").
		Return("cont-id", nil)

	err := runSyncStepUntil(cfg, "", time.Now().Add(-time.Second))
	require.NoError(t, err)
	host.KVStoreMock.AssertExpectations(t)
	host.SchedulerMock.AssertExpectations(t)
}

func TestRunSyncStepUntil_ContinuationRefreshesGuardNotChecks(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	allowSweepRecordWrites()

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), mock.Anything, "").Return("cont-id", nil)

	err := runSyncStepUntil(cfg, `{"lib":0,"user":0,"off":3,"start":"2026-06-01T00:00:00Z"}`, time.Now().Add(-time.Second))
	require.NoError(t, err)
	host.KVStoreMock.AssertNotCalled(t, "Get", kvKeySweepActive())
	host.KVStoreMock.AssertExpectations(t)
	host.SchedulerMock.AssertExpectations(t)
}

// ─── LastScanAt gate ──────────────────────────────────────────────────────────

// TestRunSyncChunk_GateSkipsUnchangedLibrary proves the gate skips ALL song
// paging for a pair whose library has not been rescanned since our last sweep,
// and does NOT advance the stored threshold (nothing was processed).
func TestRunSyncChunk_GateSkipsUnchangedLibrary(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lastScan := threshold.Add(-time.Hour) // Navidrome scanned BEFORE our last sweep

	host.KVStoreMock.On("Get", kvKeyLastSynced("1", "alice")).
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: lastScan.Unix()}, nil)

	cfg := PluginConfig{IncrementalSync: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
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
	allowSweepRecordWrites()

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lastScan := threshold.Add(time.Hour) // Navidrome rescanned AFTER our last sweep

	host.KVStoreMock.On("Get", kvKeyLastSynced("1", "alice")).
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: lastScan.Unix(), MountPoint: t.TempDir()}, nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)
	host.KVStoreMock.On("Set", kvKeyLastSynced("1", "alice"), mock.Anything).Return(nil)

	cfg := PluginConfig{IncrementalSync: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}

	_, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

// ─── Retries without progress ─────────────────────────────────────────────────

// TestRunSyncChunk_FetchFailureCountsAttempt proves a failing song listing
// does not re-run back to back: the attempt is counted in the cursor, which
// delays the continuation.
func TestRunSyncChunk_FetchFailureCountsAttempt(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()
	mockGetLibrary(1, t.TempDir())
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=ghost&musicFolderId=1`,
	).Return("", fmt.Errorf("user not found"))

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "ghost", RatingTagOrder: DefaultTagOrder}},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.False(t, done)
	assert.Equal(t, 1, next.Retry)
	assert.Equal(t, 0, next.User, "the pair is retried, not skipped yet")
}

// TestRunSyncChunk_FetchFailureSkipsPairAfterMaxAttempts proves the chain
// ends: the last allowed failure skips the pair and the sweep completes.
func TestRunSyncChunk_FetchFailureSkipsPairAfterMaxAttempts(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()
	mockGetLibrary(1, t.TempDir())
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=ghost&musicFolderId=1`,
	).Return("", fmt.Errorf("user not found"))

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "ghost", RatingTagOrder: DefaultTagOrder}},
	}}}

	cur := SyncCursor{Retry: maxPairAttempts - 1, PairStart: "2026-06-01T00:00:00Z"}
	next, done := runSyncChunk(cfg, cur, time.Now().Add(time.Hour))
	assert.True(t, done, "the failing pair is skipped, so the sweep finishes")
	assert.Equal(t, 0, next.Retry)
}

// TestRunSyncStepUntil_RetryIsDelayed proves a continuation that carries a
// failed attempt is scheduled with retryDelaySeconds instead of immediately.
func TestRunSyncStepUntil_RetryIsDelayed(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	allowSweepRecordWrites()
	mockGetLibrary(1, t.TempDir())

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "ghost", RatingTagOrder: DefaultTagOrder}},
	}}}
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=ghost&musicFolderId=1`,
	).Return("", fmt.Errorf("user not found"))
	host.SchedulerMock.On("ScheduleOneTime", int32(retryDelaySeconds), mock.Anything, "").Return("cont-id", nil)

	err := runSyncStepUntil(cfg, `{"lib":0,"user":0,"off":0,"start":"2026-06-01T00:00:00Z"}`, time.Now().Add(time.Hour))
	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
}

// TestRunSyncChunk_MatcherFailureIsRetriedThenSkipped proves a matcher that
// keeps failing counts attempts like a failed listing and cannot keep the
// chain alive forever.
func TestRunSyncChunk_MatcherFailureIsRetriedThenSkipped(t *testing.T) {
	resetSubsonicMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()
	mockGetLibrary(1, t.TempDir())
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "s1"}}), nil)
	host.MatcherMock.On("MatchSongs", songRefs("s1"), host.MatchOptions{}).
		Return([]*types.Track(nil), fmt.Errorf("matcher unavailable"))

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", RatingTagOrder: DefaultTagOrder}},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.False(t, done)
	assert.Equal(t, 1, next.Retry)
	assert.Equal(t, 0, next.Offset, "the page is retried from the same offset")

	next, done = runSyncChunk(cfg, SyncCursor{Retry: maxPairAttempts - 1, PairStart: "2026-06-01T00:00:00Z"}, time.Now().Add(time.Hour))
	assert.True(t, done, "the last attempt skips the pair")
	assert.Equal(t, 0, next.Retry)
}

// ─── Resume after a crash or reload ───────────────────────────────────────────

func resumeTestConfig() PluginConfig {
	return PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: DefaultTagOrder}},
	}}}
}

// TestRunSyncStepUntil_ResumesAfterCrashLoggingTheCrashPage proves a fresh
// run that finds a stale sweep record resumes at its cursor and logs every
// file read up to the end of that page.
func TestRunSyncStepUntil_ResumesAfterCrashLoggingTheCrashPage(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	stale := time.Now().Add(-2 * sweepStaleAfter).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", kvKeySweepActive()).
		Return([]byte(`{"at":"`+stale+`","cursor":{"lib":0,"user":0,"off":700,"start":"2026-06-01T00:00:00Z"}}`), true, nil)
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), `{"lib":0,"user":0,"off":700,"start":"2026-06-01T00:00:00Z","logto":1000}`, "").
		Return("cont-id", nil)

	require.NoError(t, runSyncStepUntil(resumeTestConfig(), "", time.Now().Add(-time.Second)))
	host.SchedulerMock.AssertExpectations(t)
}

// TestRunSyncStepUntil_ResumesAfterReloadWithoutLogging proves a sweep
// interrupted by a reload resumes at its cursor without the crash hunt.
func TestRunSyncStepUntil_ResumesAfterReloadWithoutLogging(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", kvKeySweepActive()).
		Return([]byte(`{"at":"`+now+`","cursor":{"lib":0,"user":0,"off":700,"start":"2026-06-01T00:00:00Z","logto":1000},"interrupted":true}`), true, nil)
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), `{"lib":0,"user":0,"off":700,"start":"2026-06-01T00:00:00Z"}`, "").
		Return("cont-id", nil)

	require.NoError(t, runSyncStepUntil(resumeTestConfig(), "", time.Now().Add(-time.Second)))
	host.SchedulerMock.AssertExpectations(t)
}

// TestRunSyncStepUntil_RefusedContinuationIsMarkedInterrupted proves a chain
// the host refused to continue is not mistaken for a crash later.
func TestRunSyncStepUntil_RefusedContinuationIsMarkedInterrupted(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	host.KVStoreMock.On("Get", kvKeySweepActive()).Return([]byte(nil), false, nil).Once()
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.MatchedBy(func(v []byte) bool {
		return !strings.Contains(string(v), `"interrupted":true`)
	})).Return(nil).Once()
	host.KVStoreMock.On("Get", kvKeySweepActive()).Return([]byte(`{"at":"2026-06-01T00:00:00Z"}`), true, nil).Once()
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.MatchedBy(func(v []byte) bool {
		return strings.Contains(string(v), `"interrupted":true`)
	})).Return(nil).Once()
	host.SchedulerMock.On("ScheduleOneTime", int32(0), mock.Anything, "").Return("", fmt.Errorf("scheduler full"))

	require.Error(t, runSyncStepUntil(resumeTestConfig(), "", time.Now().Add(-time.Second)))
	host.KVStoreMock.AssertExpectations(t)
}

// TestProcessPairChunk_StoresPositionAtEveryPage proves the sweep record is
// refreshed with the cursor at the start of a page, so a crash on that page
// resumes there.
func TestProcessPairChunk_StoresPositionAtEveryPage(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.MatchedBy(func(v []byte) bool {
		return strings.Contains(string(v), `"off":0`)
	})).Return(nil).Once()
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)

	_, done := processPairChunk(LibraryConfig{LibraryID: "1"}, UserConfig{Username: "alice", SkipAlreadyRated: true},
		PluginConfig{}, SyncCursor{}, time.Time{}, time.Now().Add(time.Hour), libraryMount{})
	assert.True(t, done)
	host.KVStoreMock.AssertExpectations(t)
}

// TestProcessPairChunk_FileReadLoggingStopsAfterTheCrashPage proves the
// logging of every file read ends once the cursor passes the crash page.
func TestProcessPairChunk_FileReadLoggingStopsAfterTheCrashPage(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	allowSweepRecordWrites()
	songs := []subsonicSong{{ID: "a1", UserRating: 5}, {ID: "a2", UserRating: 5}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	lib, user := LibraryConfig{LibraryID: "1"}, UserConfig{Username: "alice", SkipAlreadyRated: true}
	past := time.Now().Add(-time.Second) // yields after one song

	next, _ := processPairChunk(lib, user, PluginConfig{}, SyncCursor{LogReadsUntil: 500}, time.Time{}, past, libraryMount{})
	assert.Equal(t, 500, next.LogReadsUntil, "still on the crash page")

	next, _ = processPairChunk(lib, user, PluginConfig{}, SyncCursor{LogReadsUntil: 1}, time.Time{}, past, libraryMount{})
	assert.Equal(t, 0, next.LogReadsUntil, "past the crash page: normal again")
}
