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

// fileIndexResult is a memoised resolve+walk outcome: the file index for a
// library plus whether the mount could be resolved and read. A "false" entry
// means we already logged the failure and the caller should skip the pair
// without saving the threshold.
type fileIndexResult struct {
	index map[string][]fileEntry
	ok    bool
}

// cachedFileIndex returns the file index for libraryID, building it on first
// access and memoising the result for the lifetime of a single runSyncChunk
// call. State does not survive across callbacks, so the cache is created fresh
// per call. N users of one library therefore share one walk; an unchanged
// library that the LastScanAt gate skips never reaches this cache at all.
func cachedFileIndex(cache map[string]fileIndexResult, libraryID string) (map[string][]fileEntry, bool) {
	if r, found := cache[libraryID]; found {
		return r.index, r.ok
	}
	idx, ok := resolveAndIndex(libraryID)
	cache[libraryID] = fileIndexResult{index: idx, ok: ok}
	return idx, ok
}

// resolveAndIndex is the uncached variant: resolve the library's mount and
// walk it. Both stages fail-closed by skipping the pair (caller responsibility):
// without a real file index we cannot match songs to files and any read
// attempt would just regress to the s.Path bug.
func resolveAndIndex(libraryID string) (map[string][]fileEntry, bool) {
	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot access filesystem for library=%q: %v – skipping pair", libraryID, err))
		return nil, false
	}
	idx, err := buildFileIndex(mountPoint)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot read library mount %q – skipping pair", mountPoint))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: read mount %q error: %q", mountPoint, err.Error()))
		return nil, false
	}
	logDebug(fmt.Sprintf(
		"nd-rating-sync: indexed mount %q for library=%q – %d size buckets",
		mountPoint, libraryID, len(idx)))
	return idx, true
}
