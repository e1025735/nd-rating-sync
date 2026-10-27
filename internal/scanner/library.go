package scanner

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// getLibrary fetches a configured library from the host. The ID must be
// numeric (Navidrome's internal library IDs are); anything else never
// reaches the host.
func getLibrary(libraryID string) (*host.Library, error) {
	id, err := strconv.Atoi(strings.TrimSpace(libraryID))
	if err != nil {
		return nil, fmt.Errorf("library ID %q is not numeric", libraryID)
	}
	lib, err := host.LibraryGetLibrary(int32(id))
	if err != nil {
		return nil, err
	}
	if lib == nil {
		return nil, fmt.Errorf("library %d not found", id)
	}
	return lib, nil
}

// libLookup is a memoised getLibrary outcome.
type libLookup struct {
	lib *host.Library
	err error
}

// cachedLibrary fetches each library at most once per callback, so the
// LastScanAt gate and the mount lookup of N users of one library cost one
// host call. Globals do not survive between callbacks, so the cache is
// scoped to one runSyncChunk invocation.
func cachedLibrary(cache map[string]libLookup, libraryID string) (*host.Library, error) {
	if r, ok := cache[libraryID]; ok {
		return r.lib, r.err
	}
	lib, err := getLibrary(libraryID)
	cache[libraryID] = libLookup{lib: lib, err: err}
	return lib, err
}

// libraryLastScan returns the wall-clock time of Navidrome's most recent scan of
// the given library, used to skip (library, user) pairs whose library has not
// been rescanned since our last successful sweep. It fails open (false) on any
// uncertainty: a blank or non-numeric ID (file access needs a numeric one
// anyway), a host error, or a library that was never scanned.
func libraryLastScan(libraryID string) (time.Time, bool) {
	return lastScanOf(libraryID, getLibrary)
}

func cachedLibraryLastScan(cache map[string]libLookup, libraryID string) (time.Time, bool) {
	return lastScanOf(libraryID, func(id string) (*host.Library, error) { return cachedLibrary(cache, id) })
}

func lastScanOf(libraryID string, get func(string) (*host.Library, error)) (time.Time, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan start lib=%q", libraryID))
	lib, err := get(libraryID)
	if err != nil {
		logDebug(fmt.Sprintf("nd-rating-sync: last scan of library %q unknown (%q) – gate open (will page)", libraryID, err.Error()))
		return time.Time{}, false
	}
	if lib.LastScanAt == 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan stop, never scanned lib=%q", libraryID))
		return time.Time{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: libraryLastScan done lib=%q last_scan_at=%d", libraryID, lib.LastScanAt))
	return time.Unix(lib.LastScanAt, 0), true
}

// libraryMount is where a library's files are inside the plugin sandbox.
type libraryMount struct {
	id         int32
	mountPoint string // read-only sandbox mount, e.g. /libraries/1
	hostPath   string // the library's root on the Navidrome host
}

// resolveMount maps a configured library ID to its in-sandbox mount. An empty
// MountPoint means the plugin lacks filesystem access to the library.
func resolveMount(cache map[string]libLookup, libraryID string) (libraryMount, error) {
	lib, err := cachedLibrary(cache, libraryID)
	if err != nil {
		return libraryMount{}, err
	}
	if lib.MountPoint == "" {
		return libraryMount{}, errors.New("no filesystem mount point returned (grant the 'library' permission with filesystem access and assign this library to the plugin)")
	}
	return libraryMount{id: lib.ID, mountPoint: lib.MountPoint, hostPath: lib.Path}, nil
}

// accessCheck holds the users and libraries the plugin may access, read from
// the host at most once per callback. A list the host could not return stays
// nil and allows everything, so a failed lookup never blocks a sync.
type accessCheck struct {
	loaded bool
	users  map[string]bool // lower-cased login names
	libs   map[int32]bool
}

func (a *accessCheck) load() {
	if a.loaded {
		return
	}
	a.loaded = true
	if users, err := host.UsersGetUsers(); err == nil {
		a.users = make(map[string]bool, len(users))
		for _, u := range users {
			a.users[strings.ToLower(u.UserName)] = true
		}
	} else {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot list the users granted to the plugin (%q) – usernames are not checked", err.Error()))
	}
	if libs, err := host.LibraryGetAllLibraries(); err == nil {
		a.libs = make(map[int32]bool, len(libs))
		for _, l := range libs {
			a.libs[l.ID] = true
		}
	} else {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot list the libraries assigned to the plugin (%q) – library IDs are not checked", err.Error()))
	}
}

// problem returns why the plugin cannot sync username in libraryID, or ""
// when it can (or when that cannot be told). Navidrome logins are
// case-insensitive, so usernames are compared that way. A non-numeric
// library ID is left to the mount lookup, which reports it.
func (a *accessCheck) problem(libraryID, username string) string {
	a.load()
	if a.libs != nil {
		if id, err := strconv.Atoi(strings.TrimSpace(libraryID)); err == nil && !a.libs[int32(id)] {
			return fmt.Sprintf("library %d does not exist or is not assigned to the plugin (Library Access)", id)
		}
	}
	if a.users != nil && !a.users[strings.ToLower(username)] {
		return fmt.Sprintf("user %q is not known to Navidrome or not granted under the plugin's User Access tab", username)
	}
	return ""
}
