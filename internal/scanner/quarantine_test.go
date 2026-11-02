//go:build !tinygo

package scanner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
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
// the sweep resumes right after it with the rest of the page still watched.
// A reload in between (interrupted) keeps that evidence.
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
			return strings.Contains(payload, `"off":701`) && strings.Contains(payload, `"logto":1000`) && !strings.Contains(payload, "reading")
		}), "").Return("cont-id", nil).Once()

		require.NoError(t, runSyncStepUntil(resumeTestConfig(), "", time.Now().Add(-time.Second)))
		host.KVStoreMock.AssertExpectations(t)
		host.SchedulerMock.AssertExpectations(t)
	}
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
