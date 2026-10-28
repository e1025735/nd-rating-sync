package scanner

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/e1025735/nd-rating-sync/internal/logging"
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

// hostLookups reads what runSyncChunk needs from the host about libraries
// and users, each at most once per callback: N users of one library cost one
// LibraryGetLibrary call for the LastScanAt gate and the mount together, and
// the lists of granted users and libraries are read on first use. Globals do
// not survive between callbacks, so each runSyncChunk call makes its own.
type hostLookups struct {
	libs map[string]libLookup

	// The users and libraries the plugin may access. A list the host could
	// not return stays nil and allows everything, so a failed lookup never
	// blocks a sync.
	accessLoaded bool
	users        map[string]bool // lower-cased login names
	accessible   map[int32]bool
}

// libLookup is a memoised getLibrary outcome.
type libLookup struct {
	lib *host.Library
	err error
}

func newHostLookups() *hostLookups {
	return &hostLookups{libs: map[string]libLookup{}}
}

// library is getLibrary, fetched at most once per library ID.
func (h *hostLookups) library(libraryID string) (*host.Library, error) {
	if r, ok := h.libs[libraryID]; ok {
		return r.lib, r.err
	}
	lib, err := getLibrary(libraryID)
	h.libs[libraryID] = libLookup{lib: lib, err: err}
	return lib, err
}

// lastScan returns the wall-clock time of Navidrome's most recent scan of
// the library, used to skip (library, user) pairs whose library has not been
// rescanned since our last successful sweep. It fails open (false) on any
// uncertainty: a blank or non-numeric ID (file access needs a numeric one
// anyway), a host error, or a library that was never scanned.
func (h *hostLookups) lastScan(libraryID string) (time.Time, bool) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: lastScan start lib=%q", libraryID))
	lib, err := h.library(libraryID)
	if err != nil {
		logging.Debug(fmt.Sprintf("nd-rating-sync: last scan of library %q unknown (%q) – gate open (will page)", libraryID, err.Error()))
		return time.Time{}, false
	}
	if lib.LastScanAt == 0 {
		logging.Trace(fmt.Sprintf("nd-rating-sync: lastScan stop, never scanned lib=%q", libraryID))
		return time.Time{}, false
	}
	logging.Trace(fmt.Sprintf("nd-rating-sync: lastScan done lib=%q last_scan_at=%d", libraryID, lib.LastScanAt))
	return time.Unix(lib.LastScanAt, 0), true
}

// libraryMount is where a library's files are inside the plugin sandbox.
type libraryMount struct {
	id         int32
	mountPoint string // read-only sandbox mount, e.g. /libraries/1
	hostPath   string // the library's root on the Navidrome host
}

// mount maps a configured library ID to its in-sandbox mount. An empty
// MountPoint means the plugin lacks filesystem access to the library.
func (h *hostLookups) mount(libraryID string) (libraryMount, error) {
	lib, err := h.library(libraryID)
	if err != nil {
		return libraryMount{}, err
	}
	if lib.MountPoint == "" {
		return libraryMount{}, errors.New("no filesystem mount point returned (grant the 'library' permission with filesystem access and assign this library to the plugin)")
	}
	return libraryMount{id: lib.ID, mountPoint: lib.MountPoint, hostPath: lib.Path}, nil
}

// loadAccess reads the users and libraries the plugin may access, once.
func (h *hostLookups) loadAccess() {
	if h.accessLoaded {
		return
	}
	h.accessLoaded = true
	if users, err := host.UsersGetUsers(); err == nil {
		h.users = make(map[string]bool, len(users))
		for _, u := range users {
			h.users[strings.ToLower(u.UserName)] = true
		}
	} else {
		logging.Warn(fmt.Sprintf("nd-rating-sync: cannot list the users granted to the plugin (%q) – usernames are not checked", err.Error()))
	}
	if libs, err := host.LibraryGetAllLibraries(); err == nil {
		h.accessible = make(map[int32]bool, len(libs))
		for _, l := range libs {
			h.accessible[l.ID] = true
		}
	} else {
		logging.Warn(fmt.Sprintf("nd-rating-sync: cannot list the libraries assigned to the plugin (%q) – library IDs are not checked", err.Error()))
	}
}

// problem returns why the plugin cannot sync username in libraryID, or ""
// when it can (or when that cannot be told). Navidrome logins are
// case-insensitive, so usernames are compared that way. A non-numeric
// library ID is left to the mount lookup, which reports it.
func (h *hostLookups) problem(libraryID, username string) string {
	h.loadAccess()
	if h.accessible != nil {
		if id, err := strconv.Atoi(strings.TrimSpace(libraryID)); err == nil && !h.accessible[int32(id)] {
			return fmt.Sprintf("library %d does not exist or is not assigned to the plugin (Library Access)", id)
		}
	}
	if h.users != nil && !h.users[strings.ToLower(username)] {
		return fmt.Sprintf("user %q is not known to Navidrome or not granted under the plugin's User Access tab", username)
	}
	return ""
}
