//go:build !tinygo

package scanner

import (
	"errors"
	"testing"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestHostLookupsLastScan_NumericID(t *testing.T) {
	resetLibraryMock(t)
	when := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	host.LibraryMock.On("GetLibrary", int32(7)).
		Return(&host.Library{ID: 7, LastScanAt: when.Unix()}, nil)

	got, ok := newHostLookups().lastScan("7")
	require.True(t, ok)
	assert.True(t, when.Equal(got), "want=%s got=%s", when, got)
	host.LibraryMock.AssertExpectations(t)
}

func TestHostLookupsLastScan_NonNumericIDFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	// No expectation registered: a non-numeric ID must never reach the host.
	_, ok := newHostLookups().lastScan("lib1")
	assert.False(t, ok, "non-numeric ID → gate open (false)")
	host.LibraryMock.AssertNotCalled(t, "GetLibrary", mock.Anything)
}

func TestHostLookupsLastScan_HostErrorFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return((*host.Library)(nil), errors.New("library not accessible"))

	_, ok := newHostLookups().lastScan("1")
	assert.False(t, ok, "host error → gate open (false)")
}

func TestHostLookupsLastScan_NeverScannedFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	host.LibraryMock.On("GetLibrary", int32(1)).
		Return(&host.Library{ID: 1, LastScanAt: 0}, nil)

	_, ok := newHostLookups().lastScan("1")
	assert.False(t, ok, "LastScanAt==0 (never scanned) → gate open (false)")
}

// File access needs a numeric library ID, so an empty one never reaches the
// host either.
func TestHostLookupsLastScan_EmptyIDFailsOpen(t *testing.T) {
	resetLibraryMock(t)
	_, ok := newHostLookups().lastScan("")
	assert.False(t, ok)
	host.LibraryMock.AssertNotCalled(t, "GetLibrary", mock.Anything)
	host.LibraryMock.AssertNotCalled(t, "GetAllLibraries")
}
