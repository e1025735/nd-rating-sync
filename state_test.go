package main

import (
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
	t.Cleanup(func() {
		host.KVStoreMock.ExpectedCalls = nil
		host.KVStoreMock.Calls = nil
	})
}

// ─── kvKeyLastSynced ──────────────────────────────────────────────────────────

func TestKVKeyLastSynced_IncludesLibraryAndUser(t *testing.T) {
	assert.Equal(t, "last-synced:lib1:alice", kvKeyLastSynced("lib1", "alice"))
	// Different tuples must produce distinct keys.
	assert.NotEqual(t,
		kvKeyLastSynced("lib1", "alice"),
		kvKeyLastSynced("lib1", "bob"))
	assert.NotEqual(t,
		kvKeyLastSynced("lib1", "alice"),
		kvKeyLastSynced("lib2", "alice"))
}

// ─── loadLastSynced ───────────────────────────────────────────────────────────

func TestLoadLastSynced_KeyMissing(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte(nil), false, nil)

	got := loadLastSynced("lib1", "alice")
	assert.True(t, got.IsZero())
	host.KVStoreMock.AssertExpectations(t)
}

func TestLoadLastSynced_RoundTrip(t *testing.T) {
	resetKVStoreMock(t)
	want := time.Date(2026, 5, 7, 12, 30, 45, 123456000, time.UTC)
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte(want.Format(time.RFC3339Nano)), true, nil)

	got := loadLastSynced("lib1", "alice")
	assert.True(t, want.Equal(got), "want=%s got=%s", want, got)
}

func TestLoadLastSynced_MalformedFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte("not a real timestamp"), true, nil)

	got := loadLastSynced("lib1", "alice")
	assert.True(t, got.IsZero())
}

func TestLoadLastSynced_KVErrorFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte(nil), false, errors.New("kvstore unavailable"))

	got := loadLastSynced("lib1", "alice")
	assert.True(t, got.IsZero())
}

func TestLoadLastSynced_EmptyValueFallsBackToZero(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").
		Return([]byte{}, true, nil)

	got := loadLastSynced("lib1", "alice")
	assert.True(t, got.IsZero())
}

// ─── saveLastSynced ───────────────────────────────────────────────────────────

func TestSaveLastSynced_WritesUTCFormatted(t *testing.T) {
	resetKVStoreMock(t)
	// Local-zone timestamp; saveLastSynced must normalise to UTC RFC3339Nano.
	when := time.Date(2026, 5, 7, 12, 0, 0, 0, time.FixedZone("X", 7200))
	host.KVStoreMock.On("Set",
		"last-synced:lib1:alice",
		[]byte(when.UTC().Format(time.RFC3339Nano)),
	).Return(nil)

	saveLastSynced("lib1", "alice", when)
	host.KVStoreMock.AssertExpectations(t)
}

func TestSaveLastSynced_KVErrorIsSwallowed(t *testing.T) {
	resetKVStoreMock(t)
	host.KVStoreMock.On("Set", "last-synced:lib1:alice", mock.Anything).
		Return(errors.New("disk full"))

	// Must not panic / propagate; failure is logged only.
	saveLastSynced("lib1", "alice", time.Now())
}

// ─── round-trip via load+save ─────────────────────────────────────────────────

func TestStateRoundTrip(t *testing.T) {
	resetKVStoreMock(t)

	when := time.Now().UTC().Truncate(time.Nanosecond)
	encoded := []byte(when.Format(time.RFC3339Nano))

	host.KVStoreMock.On("Set", "last-synced:lib1:alice", encoded).Return(nil).Once()
	host.KVStoreMock.On("Get", "last-synced:lib1:alice").Return(encoded, true, nil).Once()

	saveLastSynced("lib1", "alice", when)
	got := loadLastSynced("lib1", "alice")
	require.True(t, when.Equal(got), "want=%s got=%s", when, got)
	host.KVStoreMock.AssertExpectations(t)
}
