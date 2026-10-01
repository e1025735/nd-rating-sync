package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// ─── runSyncStep ─────────────────────────────────────────────────────────────

func TestRunSyncStep_NoLibraries(t *testing.T) {
	err := runSyncStep(PluginConfig{}, "")
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

	// Empty mount: the song's size matches no file, so matchFile reports
	// not-found and the scanner must surface this as FileUnreadable.
	mount := t.TempDir()
	mockGetLibrary(1, mount)

	// Song points at a path that doesn't exist — extractStarsFromFile must
	// surface this as FileUnreadable.
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

	cfg := PluginConfig{IncrementalSync: false, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
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

	lib := LibraryConfig{LibraryID: "lib1"}
	user := UserConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}

	deadline := time.Now().Add(-time.Second) // already past
	next, pairDone := processPairChunk(lib, user, PluginConfig{}, SyncCursor{}, time.Time{}, deadline, nil, false, nil)

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

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users: []UserConfig{
			{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
			{Username: "bob", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
		},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done, "both pairs processed → sweep complete")
	assert.Equal(t, 1, next.Lib, "cursor advanced past the only library")
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestRunSyncChunk_UsesPersistentIndexWhenEnabled(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	info, err := os.Stat(path)
	require.NoError(t, err)
	mockGetLibrary(1, mount)

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Suffix: "mp3", Size: info.Size()}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	host.KVStoreMock.On("Get", libraryScanStateKey("1")).Return([]byte(nil), false, nil)
	bucketKeyName := bucketKey("1", info.Size(), "mp3")
	bucketValue, err := json.Marshal([]FileRecord{{Path: path, Mtime: info.ModTime().Unix()}})
	require.NoError(t, err)
	host.KVStoreMock.On("Get", bucketKeyName).Return([]byte(nil), false, nil).Once()
	host.KVStoreMock.On("Get", bucketKeyName).Return(bucketValue, true, nil).Once()
	host.KVStoreMock.On("Set", bucketKeyName, mock.Anything).Return(nil)
	host.KVStoreMock.On("Set", libraryScanStateKey("1"), mock.Anything).Return(nil)

	cfg := PluginConfig{CacheLibrariesFilesystemTree: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	next, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	require.True(t, done)
	assert.Equal(t, 1, next.Lib)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
}

// TestRunSyncStepUntil_ReschedulesWhenBudgetExceeded proves an unfinished sweep
// persists its cursor into a fresh one-time callback (empty scheduleID → host
// mints a unique one).
func TestRunSyncStepUntil_ReschedulesWhenBudgetExceeded(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "lib1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}
	host.KVStoreMock.On("Get", kvKeyConfigHash).Return([]byte(configHashFor(cfg)), true, nil)

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
	mockGetLibrary(1, t.TempDir())

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}
	host.KVStoreMock.On("Get", kvKeyConfigHash).Return([]byte(configHashFor(cfg)), true, nil)

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

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}
	host.KVStoreMock.On("Get", kvKeyConfigHash).Return([]byte(configHashFor(cfg)), true, nil)
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

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}
	host.KVStoreMock.On("Get", kvKeyConfigHash).Return([]byte(configHashFor(cfg)), true, nil)

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

	cfg := PluginConfig{Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}
	host.KVStoreMock.On("Get", kvKeyConfigHash).Return([]byte(configHashFor(cfg)), true, nil)
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

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lastScan := threshold.Add(-time.Hour) // Navidrome scanned BEFORE our last sweep

	host.KVStoreMock.On("Get", kvKeyLastSynced("1", "alice")).
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: lastScan.Unix()}, nil)

	cfg := PluginConfig{IncrementalSync: true, Libraries: []LibraryConfig{{
		LibraryID: "1",
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
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
		Users:     []UserConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	_, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	assert.True(t, done)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.LibraryMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}
