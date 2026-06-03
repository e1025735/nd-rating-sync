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

func resetLibraryMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.LibraryMock.ExpectedCalls = nil
		host.LibraryMock.Calls = nil
	})
}

func TestLibraryLastScan_NumericID(t *testing.T) {
	resetLibraryMock(t)
	when := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	host.LibraryMock.On("GetLibrary", int32(7)).
		Return(&host.Library{ID: 7, LastScanAt: when.Unix()}, nil)

	got, ok := libraryLastScan("7")
	require.True(t, ok)
	assert.True(t, when.Equal(got), "want=%s got=%s", when, got)
	host.LibraryMock.AssertExpectations(t)
}

func TestLibraryLastScan_NonNumericIDFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	// No expectation registered: a non-numeric ID must never reach the host.
	_, ok := libraryLastScan("lib1")
	assert.False(t, ok, "non-numeric ID → gate open (false)")
	host.LibraryMock.AssertNotCalled(t, "GetLibrary", mock.Anything)
}

func TestLibraryLastScan_HostErrorFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return((*host.Library)(nil), errors.New("library not accessible"))

	_, ok := libraryLastScan("1")
	assert.False(t, ok, "host error → gate open (false)")
}

func TestLibraryLastScan_NeverScannedFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: 0}, nil)

	_, ok := libraryLastScan("1")
	assert.False(t, ok, "LastScanAt==0 (never scanned) → gate open (false)")
}

func TestLibraryLastScan_AllLibrariesUsesNewest(t *testing.T) {
	resetLibraryMock(t)
	older := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	host.LibraryMock.On("GetAllLibraries").Return([]host.Library{
		{ID: 1, LastScanAt: older.Unix()},
		{ID: 2, LastScanAt: newer.Unix()},
	}, nil)

	got, ok := libraryLastScan("")
	require.True(t, ok)
	assert.True(t, newer.Equal(got), "empty ID → newest LastScanAt across all libraries")
	host.LibraryMock.AssertExpectations(t)
}
