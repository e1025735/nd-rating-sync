//go:build !tinygo

package scanner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// crashedEntry is the quarantine entry of the file at path as it is now.
func crashedEntry(t *testing.T, path string) kvadapter.CrashedFile {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return kvadapter.CrashedFile{Path: path, MTime: info.ModTime().UnixNano(), Size: info.Size()}
}

// recordContains matches a sweep-record write whose value contains every s.
func recordContains(s ...string) interface{} {
	return mock.MatchedBy(func(v []byte) bool {
		for _, want := range s {
			if !strings.Contains(string(v), want) {
				return false
			}
		}
		return true
	})
}

// ─── Watching during a crash hunt ─────────────────────────────────────────────

// During a crash hunt every file is stored in the sweep record, with the
// song's own position, before it is accessed; outside a hunt nothing is.
func TestProcessPairChunk_HuntStoresEachFileBeforeAccess(t *testing.T) {
	for _, hunt := range []bool{true, false} {
		resetSubsonicMock(t)
		resetKVStoreMock(t)
		resetMatcherMock(t)
		mount := t.TempDir()
		writeFMPSFileAt(t, mount, "song.mp3", "0.6")
		mockMatch([]string{"s1"}, trackAt("s1", 1, "song.mp3"))
		host.SubsonicAPIMock.On("Call",
			`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
		).Return(subsonicOK([]subsonicSong{{ID: "s1", Title: "Song", Suffix: "mp3"}}), nil)
		host.SubsonicAPIMock.On("Call", "setRating?id=s1&rating=3&u=alice").Return(`{"subsonic-response":{"status":"ok"}}`, nil)
		// The path is the located one (the mount joined with the matcher's path).
		watched := host.KVStoreMock.On("Set", kvKeySweepActive(), recordContains(`"off":0`, `"reading":{"id":"s1","path":`, `song.mp3"}`)).Return(nil)
		host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)

		cur := SyncCursor{}
		if hunt {
			cur.LogReadsUntil = 500
		}
		p := pairRun{lib: LibraryConfig{LibraryID: "1"}, user: UserConfig{Username: "alice", RatingTagOrder: []string{"FMPS"}},
			mount: libraryMount{id: 1, mountPoint: mount, hostPath: hostLibraryRoot}}
		_, done := processPairChunk(p, cur, time.Now().Add(time.Hour))
		require.True(t, done)

		if hunt {
			host.KVStoreMock.AssertCalled(t, "Set", kvKeySweepActive(), watched.Arguments.Get(1))
		} else {
			host.KVStoreMock.AssertNotCalled(t, "Set", kvKeySweepActive(), watched.Arguments.Get(1))
		}
	}
}

// ─── Quarantining after a stop ────────────────────────────────────────────────

// A record that still names the file being watched means the plugin stopped
// while accessing it: the file is quarantined with its size and mtime, and
// the sweep resumes at its offset, skipping it by ID, with the rest of the
// page still watched. A reload in between (interrupted) keeps that evidence.
func TestRunSyncStepUntil_StopWhileWatchingQuarantinesTheFile(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		resetSubsonicMock(t)
		resetSchedulerMock(t)
		resetKVStoreMock(t)
		path := writeFMPSFileAt(t, t.TempDir(), "crash.mp3", "0.6")
		want := crashedEntry(t, path)

		saved := SyncCursor{Offset: 700, PairStart: "2026-06-01T00:00:00Z", LogReadsUntil: 1000, Reading: &readingSong{ID: "s1", Path: path}}
		rec, err := json.Marshal(kvadapter.SweepRecord{At: time.Now().Add(-2 * sweepStaleAfter), Cursor: json.RawMessage(saved.marshal()), Interrupted: interrupted})
		require.NoError(t, err)
		host.KVStoreMock.On("Get", kvKeySweepActive()).Return(rec, true, nil)
		host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
		host.KVStoreMock.On("Set", kvadapter.KVKeyCrashedFile("s1"), mock.MatchedBy(func(v []byte) bool {
			var got kvadapter.CrashedFile
			return json.Unmarshal(v, &got) == nil && got.Path == want.Path && got.MTime == want.MTime && got.Size == want.Size
		})).Return(nil).Once()
		host.SchedulerMock.On("ScheduleOneTime", int32(0), mock.MatchedBy(func(payload string) bool {
			return strings.Contains(payload, `"off":700`) && strings.Contains(payload, `"logto":1000`) &&
				strings.Contains(payload, `"skip":"s1"`) && !strings.Contains(payload, "reading")
		}), "").Return("cont-id", nil).Once()

		require.NoError(t, runSyncStepUntil(resumeTestConfig(), "", time.Now().Add(-time.Second)))
		host.KVStoreMock.AssertExpectations(t)
		host.SchedulerMock.AssertExpectations(t)
	}
}

// The resumed pair skips the crashed song by its ID wherever it sits now, and
// without a quarantine entry (the write may have failed); a song that moved
// into its old offset is still read.
func TestProcessPairChunk_SkipsTheCrashedSongByID(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetMatcherMock(t)
	allowSweepRecordWrites()
	mount := t.TempDir()
	writeFMPSFileAt(t, mount, "crash.mp3", "0.6")
	writeFMPSFileAt(t, mount, "moved.mp3", "0.6")
	// An earlier song disappeared: the crashed song moved from offset 1 to 0,
	// and "moved" now sits at its old offset 1.
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "crash", Suffix: "mp3"}, {ID: "moved", Suffix: "mp3"}}), nil)
	mockMatch([]string{"moved"}, trackAt("moved", 1, "moved.mp3"))
	host.SubsonicAPIMock.On("Call", "setRating?id=moved&rating=3&u=alice").Return(`{"subsonic-response":{"status":"ok"}}`, nil).Once()

	p := pairRun{lib: LibraryConfig{LibraryID: "1"}, user: UserConfig{Username: "alice", RatingTagOrder: []string{"FMPS"}},
		mount: libraryMount{id: 1, mountPoint: mount, hostPath: hostLibraryRoot}}
	_, done := processPairChunk(p, SyncCursor{Offset: 1, Skip: "crash"}, time.Now().Add(time.Hour))
	require.True(t, done)
	host.SubsonicAPIMock.AssertExpectations(t)

	// At its own offset it is skipped too, without a quarantine entry.
	resetMatcherMock(t)
	mockMatch([]string{"crash", "moved"}, trackAt("crash", 1, "crash.mp3"), trackAt("moved", 1, "moved.mp3"))
	host.SubsonicAPIMock.On("Call", "setRating?id=moved&rating=3&u=alice").Return(`{"subsonic-response":{"status":"ok"}}`, nil).Once()
	_, done = processPairChunk(p, SyncCursor{Skip: "crash"}, time.Now().Add(time.Hour))
	require.True(t, done)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=crash&rating=3&u=alice")
}

// Once the watched files are done, the record no longer names one, so a stop
// in later work of the callback does not quarantine it.
func TestProcessPairChunk_ClearsTheWatchedSongOnReturn(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetMatcherMock(t)
	mount := t.TempDir()
	writeFMPSFileAt(t, mount, "song.mp3", "0.6")
	mockMatch([]string{"s1"}, trackAt("s1", 1, "song.mp3"))
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=1`,
	).Return(subsonicOK([]subsonicSong{{ID: "s1", Suffix: "mp3"}}), nil)
	host.SubsonicAPIMock.On("Call", "setRating?id=s1&rating=3&u=alice").Return(`{"subsonic-response":{"status":"ok"}}`, nil)
	var last string
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).
		Run(func(a mock.Arguments) { last = string(a.Get(1).([]byte)) }).Return(nil)

	p := pairRun{lib: LibraryConfig{LibraryID: "1"}, user: UserConfig{Username: "alice", RatingTagOrder: []string{"FMPS"}},
		mount: libraryMount{id: 1, mountPoint: mount, hostPath: hostLibraryRoot}}
	_, done := processPairChunk(p, SyncCursor{LogReadsUntil: 500}, time.Now().Add(time.Hour))
	require.True(t, done)
	assert.NotContains(t, last, "reading")
	assert.Contains(t, last, `"off":1`)
}

// ─── Skipping quarantined files ──────────────────────────────────────────────

func quarantineOf(songID string, f kvadapter.CrashedFile) *quarantine {
	return &quarantine{files: map[string]kvadapter.CrashedFile{songID: f}}
}

// An unchanged quarantined file is skipped, never read or written.
func TestProcessSong_QuarantinedFileIsSkipped(t *testing.T) {
	resetSubsonicMock(t)
	path := writeFMPSFileAt(t, t.TempDir(), "crash.mp3", "0.6")

	tally := &syncTally{}
	processSong(UserConfig{Username: "alice", RatingTagOrder: []string{"FMPS"}}, PluginConfig{},
		subsonicSong{ID: "s1", Title: "Crash", Suffix: "mp3"}, time.Time{}, songFile{path: path, found: true},
		songEnv{quarantine: quarantineOf("s1", crashedEntry(t, path))}, tally)

	assert.Equal(t, 1, tally.skippedCrashed)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", mock.Anything)
}

// A quarantined file that changed since it crashed the plugin is released and
// read again.
func TestProcessSong_ChangedQuarantinedFileIsReadAgain(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	path := writeFMPSFileAt(t, t.TempDir(), "fixed.mp3", "0.6")
	old := crashedEntry(t, path)
	old.MTime -= int64(time.Hour)
	host.KVStoreMock.On("Delete", kvadapter.KVKeyCrashedFile("s1")).Return(nil).Once()
	host.SubsonicAPIMock.On("Call", "setRating?id=s1&rating=3&u=alice").Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	q := quarantineOf("s1", old)
	tally := &syncTally{}
	processSong(UserConfig{Username: "alice", RatingTagOrder: []string{"FMPS"}}, PluginConfig{},
		subsonicSong{ID: "s1", Title: "Fixed", Suffix: "mp3"}, time.Time{}, songFile{path: path, found: true},
		songEnv{quarantine: q}, tally)

	assert.Equal(t, 1, tally.rated)
	assert.Empty(t, q.files, "released for the other users too")
	host.KVStoreMock.AssertExpectations(t)
}

// A quarantined file that cannot be stat'ed stays quarantined.
func TestProcessSong_UnstattableQuarantinedFileStaysQuarantined(t *testing.T) {
	resetSubsonicMock(t)
	path := filepath.Join(t.TempDir(), "gone.mp3")

	tally := &syncTally{}
	processSong(UserConfig{Username: "alice", ClearRatingIfUntagged: true, RatingTagOrder: DefaultTagOrder}, PluginConfig{},
		subsonicSong{ID: "s1", Title: "Gone", Suffix: "mp3", UserRating: 4}, time.Time{}, songFile{path: path, found: true},
		songEnv{quarantine: quarantineOf("s1", kvadapter.CrashedFile{Path: path})}, tally)

	assert.Equal(t, 1, tally.skippedCrashed)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", mock.Anything)
}

// The quarantine is read once per callback and holds the file for every user
// of the library, so a file that crashed the plugin for one user is never
// read for the next.
func TestRunSyncChunk_QuarantineHoldsForEveryUser(t *testing.T) {
	resetSubsonicMock(t)
	resetKVStoreMock(t)
	resetLibraryMock(t)
	resetMatcherMock(t)
	mount := t.TempDir()
	path := writeFMPSFileAt(t, mount, "crash.mp3", "0.6")
	entry, err := json.Marshal(crashedEntry(t, path))
	require.NoError(t, err)
	host.KVStoreMock.On("List", "crashed:").Return([]string{kvadapter.KVKeyCrashedFile("s1")}, nil).Once()
	host.KVStoreMock.On("GetMany", []string{kvadapter.KVKeyCrashedFile("s1")}).
		Return(map[string][]byte{kvadapter.KVKeyCrashedFile("s1"): entry}, nil).Once()
	allowSweepRecordWrites()
	mockGetLibrary(1, mount)
	mockMatch([]string{"s1"}, trackAt("s1", 1, "crash.mp3"))
	for _, user := range []string{"alice", "bob"} {
		host.SubsonicAPIMock.On("Call",
			`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=`+user+`&musicFolderId=1`,
		).Return(subsonicOK([]subsonicSong{{ID: "s1", Title: "Crash", Suffix: "mp3"}}), nil)
	}

	cfg := PluginConfig{Libraries: []LibraryConfig{{LibraryID: "1", Users: []UserConfig{
		{Username: "alice", RatingTagOrder: []string{"FMPS"}},
		{Username: "bob", RatingTagOrder: []string{"FMPS"}},
	}}}}
	_, done := runSyncChunk(cfg, SyncCursor{}, time.Now().Add(time.Hour))
	require.True(t, done)

	host.KVStoreMock.AssertNumberOfCalls(t, "List", 1)
	host.KVStoreMock.AssertNumberOfCalls(t, "GetMany", 1)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=s1&rating=3&u=alice")
	host.SubsonicAPIMock.AssertNotCalled(t, "Call", "setRating?id=s1&rating=3&u=bob")
}

// ─── Pruning after a completed sweep ─────────────────────────────────────────

// mockQuarantine makes the KV store hold a quarantine entry for each song ID.
func mockQuarantine(t *testing.T, ids ...string) {
	t.Helper()
	keys := make([]string, len(ids))
	values := map[string][]byte{}
	for i, id := range ids {
		keys[i] = kvadapter.KVKeyCrashedFile(id)
		raw, err := json.Marshal(kvadapter.CrashedFile{Path: "/libraries/x/" + id + ".mp3"})
		require.NoError(t, err)
		values[keys[i]] = raw
	}
	host.KVStoreMock.On("List", "crashed:").Return(keys, nil)
	host.KVStoreMock.On("GetMany", mock.Anything).Return(values, nil)
}

// An entry no sweep would check again is deleted: the song is gone or was
// re-imported under another ID, or its library is no longer synced (removed,
// or left without users). A song in a synced library, and one Navidrome only
// marks missing, keep theirs.
func TestPruneQuarantine_DeletesEntriesNoSweepChecks(t *testing.T) {
	resetKVStoreMock(t)
	resetMatcherMock(t)
	resetLibraryMock(t)
	mockGetLibrary(1, t.TempDir()) // "keep" cannot be stat'ed there: it counts as unchanged
	mockQuarantine(t, "gone", "keep", "missing", "nousers", "removed")
	mockMatch([]string{"gone", "keep", "missing", "nousers", "removed"},
		nil,
		trackAt("keep", 1, "keep.mp3"),
		&types.Track{ID: "missing", LibraryID: 1, Path: "missing.mp3", Missing: true},
		trackAt("nousers", 2, "nousers.mp3"),
		trackAt("removed", 3, "removed.mp3"))
	for _, id := range []string{"gone", "nousers", "removed"} {
		host.KVStoreMock.On("Delete", kvadapter.KVKeyCrashedFile(id)).Return(nil).Once()
	}

	pruneQuarantine(PluginConfig{Libraries: []LibraryConfig{
		{LibraryID: "1", Users: []UserConfig{{Username: "alice"}}},
		{LibraryID: "2", Users: []UserConfig{{Username: ""}}},
	}})

	host.KVStoreMock.AssertExpectations(t)
	host.KVStoreMock.AssertNumberOfCalls(t, "Delete", 3)
}

// A file that changed since it crashed the plugin is released by the cleanup
// too, also when no sweep reaches it (every user already rated the song); an
// unchanged one stays.
func TestPruneQuarantine_ReleasesChangedFiles(t *testing.T) {
	resetKVStoreMock(t)
	resetMatcherMock(t)
	resetLibraryMock(t)
	mount := t.TempDir()
	fixed := writeFMPSFileAt(t, mount, "fixed.mp3", "0.6")
	broken := writeFMPSFileAt(t, mount, "broken.mp3", "0.6")
	mockGetLibrary(1, mount)

	old := crashedEntry(t, fixed)
	old.MTime -= int64(time.Hour)
	entries := map[string]kvadapter.CrashedFile{"fixed": old, "broken": crashedEntry(t, broken)}
	values := map[string][]byte{}
	var keys []string
	for id, f := range entries {
		raw, err := json.Marshal(f)
		require.NoError(t, err)
		keys = append(keys, kvadapter.KVKeyCrashedFile(id))
		values[kvadapter.KVKeyCrashedFile(id)] = raw
	}
	host.KVStoreMock.On("List", "crashed:").Return(keys, nil)
	host.KVStoreMock.On("GetMany", mock.Anything).Return(values, nil)
	mockMatch([]string{"broken", "fixed"}, trackAt("broken", 1, "broken.mp3"), trackAt("fixed", 1, "fixed.mp3"))
	host.KVStoreMock.On("Delete", kvadapter.KVKeyCrashedFile("fixed")).Return(nil).Once()

	pruneQuarantine(resumeTestConfig())

	host.KVStoreMock.AssertExpectations(t)
	host.KVStoreMock.AssertNotCalled(t, "Delete", kvadapter.KVKeyCrashedFile("broken"))
	host.LibraryMock.AssertNumberOfCalls(t, "GetLibrary", 1)
}

// A failed lookup deletes nothing; an empty quarantine asks nothing.
func TestPruneQuarantine_FailedLookupKeepsEverything(t *testing.T) {
	resetKVStoreMock(t)
	resetMatcherMock(t)
	mockQuarantine(t, "s1")
	host.MatcherMock.On("MatchSongs", songRefs("s1"), host.MatchOptions{}).Return([]*types.Track(nil), errors.New("matcher down"))

	pruneQuarantine(resumeTestConfig())
	host.KVStoreMock.AssertNotCalled(t, "Delete", mock.Anything)

	resetKVStoreMock(t)
	resetMatcherMock(t)
	host.KVStoreMock.On("List", "crashed:").Return([]string(nil), nil)
	pruneQuarantine(resumeTestConfig())
	host.MatcherMock.AssertNotCalled(t, "MatchSongs", mock.Anything, mock.Anything)
}

// ─── Cleanup after a completed sweep ─────────────────────────────────────────

// mockCleanupKV allows the KV calls of a completed sweep's cleanup.
func mockCleanupKV() {
	host.KVStoreMock.On("Set", kvKeySweepActive(), mock.Anything).Return(nil)
	host.KVStoreMock.On("List", "last-synced:").Return([]string(nil), nil)
	host.KVStoreMock.On("List", "crashed:").Return([]string(nil), nil)
	host.KVStoreMock.On("Delete", kvKeySweepActive()).Return(nil)
}

// A completed sweep prunes the quarantine. The cursor lies past the last
// library, so no pair is processed and the only quarantine listing is the
// cleanup's.
func TestRunSyncStepUntil_CompletedSweepPrunesTheQuarantine(t *testing.T) {
	resetKVStoreMock(t)
	resetSchedulerMock(t)
	mockCleanupKV()

	require.NoError(t, runSyncStepUntil(resumeTestConfig(), `{"lib":1,"user":0,"off":0,"start":""}`, time.Now().Add(time.Hour)))

	host.KVStoreMock.AssertCalled(t, "List", "crashed:")
	host.KVStoreMock.AssertCalled(t, "List", "last-synced:")
	host.KVStoreMock.AssertCalled(t, "Delete", kvKeySweepActive())
	host.SchedulerMock.AssertNotCalled(t, "ScheduleOneTime", mock.Anything, mock.Anything, mock.Anything)
}

// With too little budget left, the cleanup is handed to a continuation whose
// cursor lies past the last library; the sweep record stays until it ran.
func TestRunSyncStepUntil_CleanupWithoutBudgetRunsInAContinuation(t *testing.T) {
	resetKVStoreMock(t)
	resetSchedulerMock(t)
	mockCleanupKV()
	host.SchedulerMock.On("ScheduleOneTime", int32(0), mock.MatchedBy(func(payload string) bool {
		return strings.Contains(payload, `"lib":1`)
	}), "").Return("cont-id", nil).Once()

	require.NoError(t, runSyncStepUntil(resumeTestConfig(), `{"lib":1,"user":0,"off":0,"start":""}`, time.Now().Add(cleanupReserve/2)))

	host.SchedulerMock.AssertExpectations(t)
	host.KVStoreMock.AssertNotCalled(t, "List", "crashed:")
	host.KVStoreMock.AssertNotCalled(t, "List", "last-synced:")
	host.KVStoreMock.AssertNotCalled(t, "Delete", kvKeySweepActive())
}
