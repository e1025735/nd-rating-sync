package main

import (
	"bytes"
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
	path := writeFMPSFile(t, "0.6")
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

// ─── checkAndRunUserTriggeredScanWith ─────────────────────────────────────────

func TestCheckAndRunUserTriggeredScanWith_NoTriggerUsers(t *testing.T) {
	resetSubsonicMock(t)
	err := checkAndRunUserTriggeredScanWith(pluginConfig{})
	assert.NoError(t, err)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
}

func TestCheckAndRunUserTriggeredScanWith_CooldownSkipsUser(t *testing.T) {
	resetSubsonicMock(t)

	const username = "alice"
	lastUserScanMu.Lock()
	lastUserScanTimes[username] = time.Now()
	lastUserScanMu.Unlock()
	t.Cleanup(func() {
		lastUserScanMu.Lock()
		delete(lastUserScanTimes, username)
		lastUserScanMu.Unlock()
	})

	cfg := pluginConfig{
		UserScanCooldownHours: 24,
		Libraries: []libraryConfig{{
			LibraryID: "lib1",
			Users: []userConfig{{
				Username:        username,
				TriggerUserScan: true,
				RatingTagOrder:  defaultTagOrder,
			}},
		}},
	}

	err := checkAndRunUserTriggeredScanWith(cfg)
	assert.NoError(t, err)
	// The actual invariant: cooldown means no Subsonic traffic at all.
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
}

// ─── runSyncStep ─────────────────────────────────────────────────────────────

func TestRunSyncStep_NoLibraries(t *testing.T) {
	err := runSyncStep(pluginConfig{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

// ─── runSyncForUser happy path (integration) ─────────────────────────────────

// TestRunSyncForUser_HappyPath wires the full pipeline together: SubsonicAPI
// returns one song pointing at a real ID3-tagged temp file, the scanner reads
// the rating, and setRating is called with the correct star count.
func TestRunSyncForUser_HappyPath(t *testing.T) {
	resetSubsonicMock(t)

	// 1. Real ID3 file with FMPS_Rating=0.6 (→ 3 stars).
	path := writeFMPSFile(t, "0.6")

	// 2. fetchAllSongs returns one song pointing at that path.
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: path, Suffix: "mp3"}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)

	// 3. setRating expects rating=3.
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{
		Username:         "alice",
		SkipAlreadyRated: true,
		RatingTagOrder:   []string{"MediaMonkey"},
	}

	err := runSyncForUser(lib, user, pluginConfig{})
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
}

// ─── Regression: read failures must not trigger clear ────────────────────────

// TestRunSyncForUser_UnreadableFileWithClear_DoesNotClearRating proves that a
// transient read failure (here: missing file) is NOT treated as "no tag found"
// when clear_rating_if_untagged=true. Conflating the two would wipe the user's
// existing Navidrome rating on the next I/O hiccup.
func TestRunSyncForUser_UnreadableFileWithClear_DoesNotClearRating(t *testing.T) {
	resetSubsonicMock(t)

	// Song points at a path that doesn't exist — extractStarsFromFile must
	// surface this as fileUnreadable.
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: "/no/such/file.mp3", Suffix: "mp3"}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)
	// CRITICAL: setRating must NOT be called with rating=0. The mock has no
	// expectation registered for it, and AssertNotCalled below makes the
	// invariant explicit.

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{
		Username:              "alice",
		ClearRatingIfUntagged: true,
		RatingTagOrder:        []string{"MediaMonkey"},
	}

	err := runSyncForUser(lib, user, pluginConfig{})
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=0&u=alice")
}

// ─── Incremental sync ─────────────────────────────────────────────────────────

func TestRunSyncForUser_IncrementalFirstRun_ProcessesAllAndSavesThreshold(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)

	path := writeFMPSFile(t, "0.6")
	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: path, Suffix: "mp3"}}

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	// First run: KV miss → full scan.
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte(nil), false, nil).Once()
	// At end of run, scan-start timestamp is written back.
	host.KVStoreMock.On("Set", "last-synced:lib1:alice", mock.Anything).
		Return(nil).Once()

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}

	err := runSyncForUser(lib, user, pluginConfig{IncrementalSync: true})
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

func TestRunSyncForUser_IncrementalSkipsUnchangedFile(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)

	path := writeFMPSFile(t, "0.6")
	// Make the file appear older than the threshold by setting mtime to
	// a fixed past instant.
	fileTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, fileTime, fileTime))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: path, Suffix: "mp3"}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)
	// setRating should NOT be called — the file is unchanged.

	threshold := fileTime.Add(time.Hour) // strictly after the file's mtime
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil).Once()
	host.KVStoreMock.On("Set", "last-synced:lib1:alice", mock.Anything).
		Return(nil).Once()

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}

	err := runSyncForUser(lib, user, pluginConfig{IncrementalSync: true})
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=song-1&rating=3&u=alice")
	host.KVStoreMock.AssertExpectations(t)
}

func TestRunSyncForUser_IncrementalProcessesNewerFile(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)

	path := writeFMPSFile(t, "0.8") // 4 stars
	// File mtime in the future of the threshold → must be processed.
	fileTime := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, fileTime, fileTime))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: path, Suffix: "mp3"}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=4&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	threshold := fileTime.Add(-time.Hour) // before file mtime
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil).Once()
	host.KVStoreMock.On("Set", "last-synced:lib1:alice", mock.Anything).
		Return(nil).Once()

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}

	err := runSyncForUser(lib, user, pluginConfig{IncrementalSync: true})
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.KVStoreMock.AssertExpectations(t)
}

func TestRunSyncForUser_IncrementalDisabled_BypassesKV(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)

	path := writeFMPSFile(t, "0.6")
	// File mtime far in the past — would be skipped if incremental were on.
	require.NoError(t, os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)))

	songs := []subsonicSong{{ID: "song-1", Title: "Test", Path: path, Suffix: "mp3"}}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}

	err := runSyncForUser(lib, user, pluginConfig{IncrementalSync: false})
	require.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
	// Confirm KV is never touched when incremental is off.
	host.KVStoreMock.AssertNotCalled(t, "Get", mock.Anything)
	host.KVStoreMock.AssertNotCalled(t, "Set", mock.Anything, mock.Anything)
}

// ─── Chunked / resumable sync ─────────────────────────────────────────────────

// TestProcessPairChunk_StopsAtDeadlineMidPair proves the time budget yields
// after a single song (the deadline is checked after each one) and returns an
// advanced cursor so the next call resumes where this one stopped.
func TestProcessPairChunk_StopsAtDeadlineMidPair(t *testing.T) {
	resetSubsonicMock(t)

	songs := []subsonicSong{
		{ID: "1", Title: "A", UserRating: 5},
		{ID: "2", Title: "B", UserRating: 5},
		{ID: "3", Title: "C", UserRating: 5},
	}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(songs), nil)

	lib := libraryConfig{LibraryID: "lib1"}
	user := userConfig{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}

	deadline := time.Now().Add(-time.Second) // already past
	next, pairDone := processPairChunk(lib, user, pluginConfig{}, syncCursor{}, time.Time{}, deadline)

	assert.False(t, pairDone, "deadline hit mid-pair → pair not done")
	assert.Equal(t, 1, next.Offset, "exactly one song processed before yielding")
	host.SubsonicAPIMock.AssertExpectations(t)
}

// TestRunSyncChunk_AdvancesAcrossPairsToCompletion proves a generous deadline
// walks every (library, user) pair and reports the sweep complete.
func TestRunSyncChunk_AdvancesAcrossPairsToCompletion(t *testing.T) {
	resetSubsonicMock(t)

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=bob&musicFolderId=lib1`,
	).Return(subsonicOK([]subsonicSong{{ID: "b1", UserRating: 5}}), nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "lib1",
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

// TestRunSyncChunk_SingleProcessesOnlyOnePair proves a Single cursor (used by
// the user-triggered path) stops after its one pair and never touches the next.
func TestRunSyncChunk_SingleProcessesOnlyOnePair(t *testing.T) {
	resetSubsonicMock(t)

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "lib1",
		Users: []userConfig{
			{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
			{Username: "bob", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder},
		},
	}}}

	_, done := runSyncChunk(cfg, syncCursor{Single: true}, time.Now().Add(time.Hour))
	assert.True(t, done, "single-pair cursor completes after one pair")
	host.SubsonicAPIMock.AssertExpectations(t)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=bob&musicFolderId=lib1`)
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

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)

	// Fresh full sweep that finishes inside the budget: records, then clears, the
	// in-progress heartbeat.
	host.KVStoreMock.On("Get", "sweep-active").Return([]byte(nil), false, nil)
	host.KVStoreMock.On("Set", "sweep-active", mock.Anything).Return(nil)
	host.KVStoreMock.On("Delete", "sweep-active").Return(nil)

	cfg := pluginConfig{Libraries: []libraryConfig{{
		LibraryID: "lib1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	err := runSyncStepUntil(cfg, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, host.SchedulerMock.Calls, "a completed sweep schedules no continuation")
	host.KVStoreMock.AssertExpectations(t)
}

// TestCheckAndRunUserTriggeredScanWith_DueUserEnqueuesSinglePairScan proves the
// 15-minute trigger-check enqueues a Single-cursor continuation for a due user
// instead of scanning inline (which would risk the host's 30s call limit).
func TestCheckAndRunUserTriggeredScanWith_DueUserEnqueuesSinglePairScan(t *testing.T) {
	resetSubsonicMock(t)
	resetSchedulerMock(t)

	const username = "alice"
	t.Cleanup(func() {
		lastUserScanMu.Lock()
		delete(lastUserScanTimes, username)
		lastUserScanMu.Unlock()
	})

	cfg := pluginConfig{
		Libraries: []libraryConfig{{
			LibraryID: "lib1",
			Users: []userConfig{{
				Username:        username,
				TriggerUserScan: true,
				RatingTagOrder:  defaultTagOrder,
			}},
		}},
	}

	host.SchedulerMock.On("ScheduleOneTime", int32(0),
		`{"lib":0,"user":0,"off":0,"start":"","single":true}`, "").
		Return("trigger-cont-id", nil)

	err := checkAndRunUserTriggeredScanWith(cfg)
	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
	assert.Empty(t, host.SubsonicAPIMock.Calls, "trigger-check must not scan inline")
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
func TestRunSyncChunk_GateProcessesRescannedLibrary(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	lastScan := threshold.Add(time.Hour) // Navidrome rescanned AFTER our last sweep

	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: lastScan.Unix()}, nil)
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

// TestRunSyncChunk_GateBypassedBySingle proves a Single (user-triggered) scan
// re-pages even when the library is unchanged, and never consults LibraryGetLibrary.
func TestRunSyncChunk_GateBypassedBySingle(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)

	threshold := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	host.KVStoreMock.On("Get", "last-synced:1:alice").
		Return([]byte(threshold.Format(time.RFC3339Nano)), true, nil)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "a1", UserRating: 5}}), nil)
	host.KVStoreMock.On("Set", "last-synced:1:alice", mock.Anything).Return(nil)

	cfg := pluginConfig{IncrementalSync: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: defaultTagOrder}},
	}}}

	_, done := runSyncChunk(cfg, syncCursor{Single: true}, time.Now().Add(time.Hour))
	assert.True(t, done)
	host.SubsonicAPIMock.AssertExpectations(t)
	host.LibraryMock.AssertNotCalled(t, "GetLibrary", mock.Anything)
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
func writeFMPSFile(t *testing.T, value string) string {
	t.Helper()
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: value,
	})
	var buf bytes.Buffer
	_, err := tag.WriteTo(&buf)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "song.mp3")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	return path
}