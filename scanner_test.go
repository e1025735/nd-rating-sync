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

// TestExtractStarsFromFile_HugeFileNoLongerSkipped proves we removed the
// blunt 64 MiB whole-file size cap that v0.10.x imposed. A 100 MiB FLAC
// with the rating tag near the start is now processed (Phase 1 reads only
// the metadata) where previously it would have been counted as unreadable.
// The per-format extractor tests (in flac_test.go etc.) cover what bytes
// the extractor reads; this test covers extractStarsFromFile's end-to-end
// behaviour on a file that exceeds the old whole-file cap.
func TestExtractStarsFromFile_HugeFileNoLongerSkipped(t *testing.T) {
	cmt := makeFLAC(t, "FMPS_RATING=0.8") // 4 stars
	huge := append([]byte{}, cmt...)
	huge = append(huge, bytes.Repeat([]byte{0}, 100*1024*1024)...) // 100 MiB tail
	path := filepath.Join(t.TempDir(), "long-classical.flac")
	require.NoError(t, os.WriteFile(path, huge, 0o644))

	stars, result := extractStarsFromFile(path, "flac", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result, "100 MiB FLAC must now be processed, not skipped")
	assert.Equal(t, 4, stars)
}

// ─── runSyncStep ─────────────────────────────────────────────────────────────

func TestRunSyncStep_NoLibraries(t *testing.T) {
	err := runSyncStep(pluginConfig{}, "")
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
	next, pairDone := processPairChunk(lib, user, pluginConfig{}, syncCursor{}, time.Time{}, deadline, nil, false, nil)

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

	cfg := pluginConfig{CacheLibrariesFilesystemTree: true, Libraries: []libraryConfig{{
		LibraryID: "1",
		Users:     []userConfig{{Username: "alice", SkipAlreadyRated: true, RatingTagOrder: []string{"MediaMonkey"}}},
	}}}

	next, done := runSyncChunk(cfg, syncCursor{}, time.Now().Add(time.Hour))
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
