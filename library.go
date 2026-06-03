package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// libraryLastScan returns the wall-clock time of Navidrome's most recent scan of
// the given library, used to skip (library, user) pairs whose library has not
// been rescanned since our last successful sweep.
//
// The bool reports whether a usable timestamp was obtained. On any failure —
// unparseable library ID, host error, or an unscanned library (LastScanAt == 0)
// — it returns (zero, false) so the caller treats the gate as OPEN and pages the
// library as before. The gate is a pure optimisation: it must never cause work
// to be skipped on uncertainty.
//
// An empty libraryID means "all libraries" (mirrors fetchSongPage): the newest
// LastScanAt across every accessible library is returned, so the pair is gated
// only when none of them has been rescanned.
func libraryLastScan(libraryID string) (time.Time, bool) {
	if libraryID == "" {
		libs, err := host.LibraryGetAllLibraries()
		if err != nil {
			logDebug(fmt.Sprintf(
				"nd-rating-sync: LibraryGetAllLibraries failed: %q – gate open (will page)", err.Error()))
			return time.Time{}, false
		}
		var newest int64
		for _, l := range libs {
			if l.LastScanAt > newest {
				newest = l.LastScanAt
			}
		}
		if newest == 0 {
			return time.Time{}, false
		}
		return time.Unix(newest, 0), true
	}

	id, err := strconv.Atoi(libraryID)
	if err != nil {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: library ID %q is not numeric (%q) – gate open (will page)", libraryID, err.Error()))
		return time.Time{}, false
	}
	lib, err := host.LibraryGetLibrary(int32(id))
	if err != nil || lib == nil {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: LibraryGetLibrary(%d) failed – gate open (will page)", id))
		return time.Time{}, false
	}
	if lib.LastScanAt == 0 {
		return time.Time{}, false
	}
	return time.Unix(lib.LastScanAt, 0), true
}

// libScanResult is a memoised libraryLastScan outcome.
type libScanResult struct {
	t     time.Time
	found bool
}

// cachedLibraryLastScan memoises libraryLastScan per libraryID for the lifetime
// of a single runSyncChunk call, so processing N users of the same library
// costs one LibraryGetLibrary host call rather than N. State does not survive
// across callbacks, so the cache is created fresh per call.
func cachedLibraryLastScan(cache map[string]libScanResult, libraryID string) (time.Time, bool) {
	if r, ok := cache[libraryID]; ok {
		return r.t, r.found
	}
	t, found := libraryLastScan(libraryID)
	cache[libraryID] = libScanResult{t: t, found: found}
	return t, found
}
