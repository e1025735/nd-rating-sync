package scanner

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// libraryLastScan returns the wall-clock time of Navidrome's most recent scan of
// the given library, used to skip (library, user) pairs whose library has not
// been rescanned since our last successful sweep. It fails open (false) on any
// uncertainty: a blank or non-numeric ID (file access needs a numeric one
// anyway), a host error, or a library that was never scanned.
func libraryLastScan(libraryID string) (time.Time, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan start lib=%q", libraryID))
	id, err := strconv.Atoi(strings.TrimSpace(libraryID))
	if err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, library is NaN lib=%q", libraryID))
		logDebug(fmt.Sprintf("nd-rating-sync: library ID %q is not numeric – gate open (will page)", libraryID))
		return time.Time{}, false
	}
	lib, err := host.LibraryGetLibrary(int32(id))
	if err != nil || lib == nil {
		logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, cannot get library lib=%q", libraryID))
		logDebug(fmt.Sprintf("nd-rating-sync: LibraryGetLibrary(%d) failed – gate open (will page)", id))
		return time.Time{}, false
	}
	if lib.LastScanAt == 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, zero library lib=%q", libraryID))
		return time.Time{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan done lib=%q last_scan_at=%d", libraryID, lib.LastScanAt))
	return time.Unix(lib.LastScanAt, 0), true
}

// libScanResult is a memoised libraryLastScan outcome.
type libScanResult struct {
	t     time.Time
	found bool
}

func cachedLibraryLastScan(cache map[string]libScanResult, libraryID string) (time.Time, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: cachedLibraryLastScan start lib=%q", libraryID))
	if r, ok := cache[libraryID]; ok {
		logTrace(fmt.Sprintf("nd-rating-sync: cachedLibraryLastScan done, cache hit lib=%q", libraryID))
		return r.t, r.found
	}
	t, found := libraryLastScan(libraryID)
	cache[libraryID] = libScanResult{t: t, found: found}
	logTrace(fmt.Sprintf("nd-rating-sync: cachedLibraryLastScan done lib=%q found=%v", libraryID, found))
	return t, found
}
