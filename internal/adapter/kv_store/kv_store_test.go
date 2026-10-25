//go:build !tinygo

package adapter

import (
	"errors"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func init() {
	host.KVStoreMock.On("Get", KVKeyConfigHash).
		Return([]byte(ConfigHashFor([]byte("default-config"))), true, nil).Maybe()
	host.KVStoreMock.On("Set", KVKeyConfigHash, mock.Anything).Return(nil).Maybe()
}

func resetKVStoreMock(t *testing.T) {
	t.Helper()
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})
	host.KVStoreMock.On("Get", KVKeyConfigHash).
		Return([]byte(ConfigHashFor([]byte("default-config"))), true, nil).Maybe()
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

func TestKVStore_PurgeStaleKVEntries_KeepsIncrementalThresholds(t *testing.T) {
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})

	host.KVStoreMock.On("DeleteByPrefix", ConfigPrefix).Return(int64(1), nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", BucketPrefix).Return(int64(2), nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", LibraryStatePrefix).Return(int64(4), nil).Once()

	deleted, err := PurgeStaleKVEntries()
	require.NoError(t, err)
	assert.Equal(t, int64(7), deleted)
	host.KVStoreMock.AssertNotCalled(t, "DeleteByPrefix", LibraryUserLastSyncPrefix)
	host.KVStoreMock.AssertExpectations(t)
}

func TestRefreshConfigHash_PurgesPluginCacheOnMismatch(t *testing.T) {
	host.KVStoreMock.ExpectedCalls = nil
	host.KVStoreMock.Calls = nil
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})

	staleHash := "stalehash"
	current := map[string]any{"a": 1}
	currentHash := ConfigHashFor(mustJSONBytes(current))
	require.NotEqual(t, staleHash, currentHash)

	host.KVStoreMock.On("Get", KVKeyConfigHash).Return([]byte(staleHash), true, nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", ConfigPrefix).Return(int64(1), nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", BucketPrefix).Return(int64(2), nil).Once()
	host.KVStoreMock.On("DeleteByPrefix", LibraryStatePrefix).Return(int64(4), nil).Once()
	host.KVStoreMock.On("Set", KVKeyConfigHash, []byte(currentHash)).Return(nil).Once()

	EnsureConfigCacheIsCurrent(current)

	host.KVStoreMock.AssertExpectations(t)
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

func TestDeleteLibraryBuckets_UsesLibraryScopedPrefix(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("DeleteByPrefix", "bucket:1:").Return(int64(3), nil).Once()

	deleted, err := DeleteLibraryBuckets("1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
	host.KVStoreMock.AssertExpectations(t)
}

func TestGetPercentageKVStorageUsage_UsedOverMax(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("GetStorageUsed").Return(int64(250_000), nil)

	pct, err := GetPercentageKVStorageUsage("1MB")
	require.NoError(t, err)
	assert.InDelta(t, 25.0, pct, 0.001)
}

func TestSweepInProgress_FreshHeartbeatIsActive(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeySweepActive()).
		Return([]byte(time.Now().UTC().Format(time.RFC3339Nano)), true, nil)
	assert.True(t, SweepInProgress(), "a recent heartbeat means a sweep is in progress")
}

func TestSweepInProgress_StaleHeartbeatFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	stale := time.Now().Add(-2 * SweepStaleAfter).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", KVKeySweepActive()).Return([]byte(stale), true, nil)
	assert.False(t, SweepInProgress(), "a heartbeat older than SweepStaleAfter is not active")
}

func TestSweepInProgress_FutureHeartbeatFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	host.KVStoreMock.On("Get", KVKeySweepActive()).Return([]byte(future), true, nil)
	assert.False(t, SweepInProgress(), "future-dated heartbeat must fail open")
}

func TestSweepInProgress_MalformedFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeySweepActive()).Return([]byte("not a timestamp"), true, nil)
	assert.False(t, SweepInProgress(), "a malformed heartbeat is treated as not active")
}

func TestSweepInProgress_MissingFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeySweepActive()).Return([]byte(nil), false, nil)
	assert.False(t, SweepInProgress(), "no heartbeat → no sweep in progress")
}

func TestSweepInProgress_KVErrorFailsOpen(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", KVKeySweepActive()).
		Return([]byte(nil), false, errors.New("kvstore unavailable"))
	assert.False(t, SweepInProgress(), "KV error must not block a sync")
}
