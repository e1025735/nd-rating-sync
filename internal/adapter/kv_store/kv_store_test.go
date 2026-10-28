//go:build !tinygo

package adapter

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func resetKVStoreMock(t *testing.T) {
	t.Helper()
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})
}

// sweepInProgress is how the plugin decides that another sweep is running.
func sweepInProgress() bool {
	r, found := LoadSweepRecord()
	return found && r.Active()
}

func TestKVKeyLastSynced_IncludesLibraryAndUser(t *testing.T) {
	assert.Equal(t, "last-synced:lib1:alice", KVKeyLastSynced("lib1", "alice"))
	assert.NotEqual(t,
		KVKeyLastSynced("lib1", "alice"),
		KVKeyLastSynced("lib1", "bob"))
	assert.NotEqual(t,
		KVKeyLastSynced("lib1", "alice"),
		KVKeyLastSynced("lib2", "alice"))
}

func TestPurgeLegacyFileIndex_KeepsIncrementalThresholds(t *testing.T) {
	resetKVStoreMock(t)

	host.KVStoreMock.On("DeleteByPrefix", "config").Return(int64(1), nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", "bucket").Return(int64(2), nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", "libraryState").Return(int64(4), nil).Once()

	deleted, err := PurgeLegacyFileIndex()
	require.NoError(t, err)
	assert.Equal(t, int64(7), deleted)
	host.KVStoreMock.AssertNotCalled(t, "DeleteByPrefix", LibraryUserLastSyncPrefix)
	host.KVStoreMock.AssertExpectations(t)
}

func TestPurgeLegacyFileIndex_StopsAtFirstError(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("DeleteByPrefix", "bucket").Return(int64(0), errors.New("kvstore unavailable")).Once()

	_, err := PurgeLegacyFileIndex()
	require.Error(t, err)
	host.KVStoreMock.AssertNotCalled(t, "DeleteByPrefix", "config")
}

func TestLoadLastSynced_KeyMissing(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).
		Return([]byte(nil), false, nil)

	got := LoadLastSynced("lib1", "alice", "fp")
	assert.True(t, got.IsZero())
	host.KVStoreMock.AssertExpectations(t)
}

func TestLoadLastSynced_RoundTrip(t *testing.T) {
	resetKVStoreMock(t)
	want := time.Date(2026, 5, 7, 12, 30, 45, 123456000, time.UTC)
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).
		Return([]byte(want.Format(time.RFC3339Nano)), true, nil)

	got := LoadLastSynced("lib1", "alice", "fp")
	assert.True(t, want.Equal(got), "want=%s got=%s", want, got)
}

func TestLoadLastSynced_MalformedFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).
		Return([]byte("not a real timestamp"), true, nil)

	got := LoadLastSynced("lib1", "alice", "fp")
	assert.True(t, got.IsZero())
}

func TestLoadLastSynced_KVErrorFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).
		Return([]byte(nil), false, errors.New("kvstore unavailable"))

	got := LoadLastSynced("lib1", "alice", "fp")
	assert.True(t, got.IsZero())
}

func TestLoadLastSynced_EmptyValueFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).
		Return([]byte{}, true, nil)

	got := LoadLastSynced("lib1", "alice", "fp")
	assert.True(t, got.IsZero())
}

func TestSaveLastSynced_WritesUTCFormatted(t *testing.T) {
	resetKVStoreMock(t)
	when := time.Date(2026, 5, 7, 12, 0, 0, 0, time.FixedZone("X", 7200))
	host.KVStoreMock.On("Set",
		KVKeyLastSynced("lib1", "alice"),
		[]byte(when.UTC().Format(time.RFC3339Nano)+" fp"),
	).Return(nil)

	SaveLastSynced("lib1", "alice", when, "fp")
	host.KVStoreMock.AssertExpectations(t)
}

func TestSaveLastSynced_KVErrorIsSwallowed(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Set", KVKeyLastSynced("lib1", "alice"), mock.Anything).
		Return(errors.New("disk full"))

	SaveLastSynced("lib1", "alice", time.Now(), "fp")
}

func TestStateRoundTrip(t *testing.T) {
	resetKVStoreMock(t)

	when := time.Now().UTC().Truncate(time.Nanosecond)
	encoded := []byte(when.Format(time.RFC3339Nano) + " fp")

	host.KVStoreMock.On("Set", KVKeyLastSynced("lib1", "alice"), encoded).Return(nil).Once()
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).Return(encoded, true, nil).Once()

	SaveLastSynced("lib1", "alice", when, "fp")
	got := LoadLastSynced("lib1", "alice", "fp")
	require.True(t, when.Equal(got), "want=%s got=%s", when, got)
	host.KVStoreMock.AssertExpectations(t)
}

// A threshold saved with other pair settings (tag order, skip/clear flags)
// forces one full scan, so the new settings reach unchanged files too.
func TestLoadLastSynced_FingerprintMismatchFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	when := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	host.KVStoreMock.On("Get", KVKeyLastSynced("lib1", "alice")).
		Return([]byte(when.Format(time.RFC3339Nano)+" old"), true, nil)

	assert.True(t, LoadLastSynced("lib1", "alice", "new").IsZero())
}

func TestSweepInProgress_FreshHeartbeatIsActive(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).
		Return([]byte(time.Now().UTC().Format(time.RFC3339Nano)), true, nil)
	assert.True(t, sweepInProgress(), "a recent heartbeat means a sweep is in progress")
}

func TestSweepInProgress_StaleHeartbeatFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	stale := time.Now().Add(-2 * SweepStaleAfter).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", SweepActiveKey).Return([]byte(stale), true, nil)
	assert.False(t, sweepInProgress(), "a heartbeat older than SweepStaleAfter is not active")
}

func TestSweepInProgress_FutureHeartbeatFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", SweepActiveKey).Return([]byte(future), true, nil)
	assert.False(t, sweepInProgress(), "future-dated heartbeat must fail open")
}

func TestSweepInProgress_MalformedFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).Return([]byte("not a timestamp"), true, nil)
	assert.False(t, sweepInProgress(), "a malformed heartbeat is treated as not active")
}

func TestSweepInProgress_MissingFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).Return([]byte(nil), false, nil)
	assert.False(t, sweepInProgress(), "no heartbeat → no sweep in progress")
}

func TestSweepInProgress_KVErrorFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).
		Return([]byte(nil), false, errors.New("kvstore unavailable"))
	assert.False(t, sweepInProgress(), "KV error must not block a sync")
}

func TestMarkSweepActive_KVErrorIsSwallowed(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Set", SweepActiveKey, mock.Anything).
		Return(errors.New("storage limit exceeded: would use 1.0 MB of 1.0 MB allowed"))

	MarkSweepActive("")
}

func TestLoadSweepRecord_ReadsCursorAndOldTimestampFormat(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).
		Return([]byte(`{"at":"2026-06-01T12:00:00Z","cursor":{"lib":1,"off":700}}`), true, nil).Once()
	r, found := LoadSweepRecord()
	require.True(t, found)
	assert.JSONEq(t, `{"lib":1,"off":700}`, string(r.Cursor))
	assert.False(t, r.Interrupted)

	// Versions <= 0.23 stored a bare timestamp: a record without cursor.
	host.KVStoreMock.On("Get", SweepActiveKey).Return([]byte("2026-06-01T12:00:00Z"), true, nil).Once()
	r, found = LoadSweepRecord()
	require.True(t, found)
	assert.Empty(t, r.Cursor)
	assert.Equal(t, 2026, r.At.Year())
}

func TestSweepRecord_InterruptedIsNeverActive(t *testing.T) {
	assert.True(t, SweepRecord{At: time.Now()}.Active())
	assert.False(t, SweepRecord{At: time.Now(), Interrupted: true}.Active())
}

func TestMarkSweepActive_StoresCursor(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Set", SweepActiveKey, mock.MatchedBy(func(v []byte) bool {
		var r SweepRecord
		return json.Unmarshal(v, &r) == nil && string(r.Cursor) == `{"off":3}` && !r.Interrupted && r.Active()
	})).Return(nil).Once()

	MarkSweepActive(`{"off":3}`)
	host.KVStoreMock.AssertExpectations(t)
}

func TestMarkSweepInterrupted_KeepsStoredCursor(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).
		Return([]byte(`{"at":"2026-06-01T12:00:00Z","cursor":{"off":700}}`), true, nil)
	host.KVStoreMock.On("Set", SweepActiveKey, mock.MatchedBy(func(v []byte) bool {
		var r SweepRecord
		return json.Unmarshal(v, &r) == nil && string(r.Cursor) == `{"off":700}` && r.Interrupted
	})).Return(nil).Once()

	MarkSweepInterrupted("")
	host.KVStoreMock.AssertExpectations(t)
}

func TestMarkSweepInterrupted_NothingToMark(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", SweepActiveKey).Return([]byte(nil), false, nil)

	MarkSweepInterrupted("")
	host.KVStoreMock.AssertNotCalled(t, "Set", mock.Anything, mock.Anything)
}

func TestDeleteStaleLastSynced_KeepsConfiguredPairs(t *testing.T) {
	resetKVStoreMock(t)
	keep := KVKeyLastSynced("1", "alice")
	gone := KVKeyLastSynced("1", "bob")
	host.KVStoreMock.On("List", "last-synced:").Return([]string{keep, gone}, nil)
	host.KVStoreMock.On("Delete", gone).Return(nil).Once()

	deleted, err := DeleteStaleLastSynced(map[string]bool{keep: true})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
	host.KVStoreMock.AssertNotCalled(t, "Delete", keep)
	host.KVStoreMock.AssertExpectations(t)
}
