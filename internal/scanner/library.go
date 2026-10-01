package scanner

import (
	"fmt"
	"strconv"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// libraryLastScan returns the wall-clock time of Navidrome's most recent scan of
// the given library, used to skip (library, user) pairs whose library has not
// been rescanned since our last successful sweep.
func libraryLastScan(libraryID string) (time.Time, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan start lib=%q", libraryID))
	if libraryID == "" {
		libs, err := host.LibraryGetAllLibraries()
		if err != nil {
			logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, cannot get libraries lib=%q", libraryID))
			logDebug(fmt.Sprintf("nd-rating-sync: LibraryGetAllLibraries failed: %q – gate open (will page)", err.Error()))
			return time.Time{}, false
		}
		var newest int64
		for _, l := range libs {
			if l.LastScanAt > newest {
				newest = l.LastScanAt
			}
		}
		if newest == 0 {
			logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, all libraries zero lib=%q", libraryID))
			return time.Time{}, false
		}
		logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan done, all libraries lib=%q", libraryID))
		return time.Unix(newest, 0), true
	}

	id, err := strconv.Atoi(libraryID)
	if err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, library is NaN lib=%q", libraryID))
		logDebug(fmt.Sprintf("nd-rating-sync: library ID %q is not numeric (%q) – gate open (will page)", libraryID, err.Error()))
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
	logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan done lib=%q", libraryID))
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
		logTrace(fmt.Sprintf("nd-rating-sync: cachedLibraryLastScan done lib=%q", libraryID))
		return r.t, r.found
	}
	t, found := libraryLastScan(libraryID)
	cache[libraryID] = libScanResult{t: t, found: found}
	logTrace(fmt.Sprintf("nd-rating-sync: cachedLibraryLastScan done lib=%q", libraryID))
	return t, found
}
