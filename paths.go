package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// File location for plugins
// ─────────────────────────
// Navidrome does NOT hand a plugin a usable filesystem path for a song: the
// Subsonic `search3` response carries either a synthesized "fake" path (built
// from tags) or, only when the player has Report Real Path enabled, the host's
// absolute path — neither of which is openable from inside the wasip1 sandbox.
//
// What a plugin CAN open is the library's mount point. With the manifest
// `library` permission and `filesystem: true`, Navidrome read-only mounts each
// assigned library at `/libraries/{id}` inside the sandbox and exposes that
// path via host.LibraryGetLibrary(...).MountPoint. We therefore walk the mount,
// index every audio file by its exact byte size, and match each Subsonic song
// to its file on size. Size is reliable because Navidrome stores the scanned
// file size and returns it in the `size` field.

// fileEntry is a real audio file discovered under a library mount point.
type fileEntry struct {
	path  string
	size  int64
	mtime time.Time
}

// resolveMountPoint maps a configured library ID to its in-sandbox mount point.
// The plugin config stores libraryId as a string, but the Library host service
// keys on the numeric ID, so we parse it here. An empty MountPoint means the
// host did not grant filesystem access (missing `library`+`filesystem:true`
// permission, or the library is not assigned to the plugin).
func resolveMountPoint(libraryID string) (string, error) {
	id, err := strconv.Atoi(strings.TrimSpace(libraryID))
	if err != nil {
		return "", fmt.Errorf("library ID %q is not numeric", libraryID)
	}
	lib, err := host.LibraryGetLibrary(int32(id))
	if err != nil {
		return "", err
	}
	if lib == nil || lib.MountPoint == "" {
		return "", errors.New("no filesystem mount point returned (grant the 'library' permission with filesystem access and assign this library to the plugin)")
	}
	return lib.MountPoint, nil
}

// isSupportedExt reports whether ext (lowercase, no dot) is a container the
// plugin can parse. Kept in sync with dispatchParser in scanner.go.
func isSupportedExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4":
		return true
	}
	return false
}

// sizeKey is the index/lookup key: exact byte size plus lowercase extension.
// Combining size with the extension keeps unrelated containers of a coincidental
// equal size in separate buckets.
func sizeKey(size int64, ext string) string {
	return strconv.FormatInt(size, 10) + ":" + ext
}

// buildFileIndex walks mountPoint recursively and indexes supported audio files
// by sizeKey. A bucket may hold more than one file when two files share an exact
// size and extension; matchFile treats that as ambiguous rather than guessing.
func buildFileIndex(mountPoint string) (map[string][]fileEntry, error) {
	index := map[string][]fileEntry{}
	err := walkAudioFiles(mountPoint, func(path string, size int64, mtime time.Time) {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		k := sizeKey(size, ext)
		index[k] = append(index[k], fileEntry{path: path, size: size, mtime: mtime})
	})
	if err != nil {
		return nil, err
	}
	return index, nil
}

// walkAudioFiles recurses root with os.ReadDir (the pattern proven by the
// artist-nfo plugin; avoids any TinyGo filepath.WalkDir edge cases) and invokes
// fn for every regular file with a supported extension. An unreadable
// sub-directory is logged and skipped so one bad folder cannot abort the scan;
// only a failure to read the root is returned as an error.
func walkAudioFiles(root string, fn func(path string, size int64, mtime time.Time)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if e.IsDir() {
			if subErr := walkAudioFiles(full, fn); subErr != nil {
				logWarn(fmt.Sprintf("nd-rating-sync: cannot read directory %q (skipping)", full))
			}
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
		if !isSupportedExt(ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fn(full, info.Size(), info.ModTime())
	}
	return nil
}

// matchFile finds the unique file for a song by (size, suffix). It deliberately
// refuses to guess: a size+suffix collision (more than one candidate) returns
// not-found, so an ambiguous match can never cause the wrong song to be rated.
// A missing match is handled by the caller as "unreadable" — never as
// "untagged" — so a file the plugin cannot locate is never cleared.
func matchFile(index map[string][]fileEntry, s subsonicSong) (fileEntry, bool) {
	cands := index[sizeKey(s.Size, strings.ToLower(s.Suffix))]
	if len(cands) != 1 {
		return fileEntry{}, false
	}
	return cands[0], true
}
