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

type ScanState struct {
	Complete    bool
	PendingDirs []string
	LastScanAt  int64
}

type FileRecord struct {
	Path  string
	Mtime int64
}

// resolveMountPoint maps a configured library ID to its in-sandbox mount point.
// The plugin config stores libraryId as a string, but the Library host service
// keys on the numeric ID, so we parse it here. An empty MountPoint means the
// host did not grant filesystem access (missing `library`+`filesystem:true`
// permission, or the library is not assigned to the plugin).
func resolveMountPoint(libraryID string) (string, error) {
	logTrace(fmt.Sprintf("nd-rating-sync: resolveMountPoint start libraryID=%q", libraryID))
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
	logTrace(fmt.Sprintf("nd-rating-sync: resolveMountPoint done libraryID=%q", libraryID))
	return lib.MountPoint, nil
}

// isSupportedExt reports whether ext (lowercase, no dot) is a container the
// plugin can parse. Kept in sync with dispatchParser in scanner.go (and the
// readAudioMetadata switch — the early check in extractStarsFromFile uses
// this same predicate to short-circuit unsupported extensions).
func isSupportedExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4", "wma":
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
func buildFileIndexWithoutCache(mountPoint string, deadline time.Time) (map[string][]fileEntry, error) {
	logTrace(fmt.Sprintf("nd-rating-sync: buildFileIndexWithoutCache start mountPoint=%q", mountPoint))
	index := map[string][]fileEntry{}
	err := walkAudioFiles(mountPoint, deadline, func(path string, size int64, mtime time.Time) {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		k := sizeKey(size, ext)
		index[k] = append(index[k], fileEntry{path: path, size: size, mtime: mtime})
	})
	if err != nil {
		return nil, err
	}
	logTrace(fmt.Sprintf("nd-rating-sync: buildFileIndexWithoutCache done mountPoint=%q", mountPoint))
	return index, nil
}

// walkAudioFiles recurses root with os.ReadDir (the pattern proven by the
// artist-nfo plugin; avoids any TinyGo filepath.WalkDir edge cases) and invokes
// fn for every regular file with a supported extension. An unreadable
// sub-directory is logged and skipped so one bad folder cannot abort the scan;
// only a failure to read the root is returned as an error.
func walkAudioFiles(root string, deadline time.Time, fn func(path string, size int64, mtime time.Time)) error {
	logTrace(fmt.Sprintf("nd-rating-sync: walkAudioFiles start root=%q", root))
	if time.Now().After(deadline) {
		logTrace(fmt.Sprintf("nd-rating-sync: walkAudioFiles stop, deadline reached root=%q", root))
		logWarn("nd-rating-sync: the deadline was reached while scanning the library. If this happens often, consider enabling cache_libraries_filesystem_tree.")
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if e.IsDir() {
			if subErr := walkAudioFiles(full, deadline, fn); subErr != nil {
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
	logTrace(fmt.Sprintf("nd-rating-sync: walkAudioFiles done root=%q", root))
	return nil
}

// matchFile finds the unique file for a song by (size, suffix). It deliberately
// refuses to guess: a size+suffix collision (more than one candidate) returns
// not-found, so an ambiguous match can never cause the wrong song to be rated.
// A missing match is handled by the caller as "unreadable" — never as
// "untagged" — so a file the plugin cannot locate is never cleared.
func matchFile(index map[string][]fileEntry, s subsonicSong) (fileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: matchFile start song=%q", s.ID))
	cands := index[sizeKey(s.Size, strings.ToLower(s.Suffix))]
	if len(cands) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFile stop, ambiguous match song=%q", s.ID))
		return fileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFile done song=%q", s.ID))
	return cands[0], true
}

func matchFileFromBucketCache(libraryID string, song subsonicSong, cache map[string][]FileRecord) (fileEntry, bool) {
	ext := strings.ToLower(song.Suffix)
	key := sizeKey(song.Size, ext)
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache start libraryID=%q song=%q size=%d ext=%q", libraryID, song.ID, song.Size, ext))
	if cache == nil {
		records, err := loadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %v", libraryID, song.Size, ext, err))
			return fileEntry{}, false
		}
		if len(records) != 1 {
			logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache stop, ambiguous bucket libraryID=%q song=%q size=%d ext=%q records=%d", libraryID, song.ID, song.Size, ext, len(records)))
			return fileEntry{}, false
		}
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache done libraryID=%q song=%q path=%q", libraryID, song.ID, records[0].Path))
		return fileEntry{path: records[0].Path, size: song.Size, mtime: time.Unix(records[0].Mtime, 0)}, true
	}

	records, found := cache[key]
	if found {
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache cache hit libraryID=%q key=%q records=%d", libraryID, key, len(records)))
	} else {
		var err error
		records, err = loadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %v", libraryID, song.Size, ext, err))
			cache[key] = nil
			return fileEntry{}, false
		}
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache loaded bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		cache[key] = records
	}
	if len(records) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache stop, ambiguous bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		return fileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache done libraryID=%q key=%q path=%q", libraryID, key, records[0].Path))
	return fileEntry{path: records[0].Path, size: song.Size, mtime: time.Unix(records[0].Mtime, 0)}, true
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
func cachedFileIndex(cache map[string]fileIndexResult, cfg pluginConfig, libraryID string, deadline time.Time) (map[string][]fileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex start libraryID=%q", libraryID))
	if r, found := cache[libraryID]; found {
		return r.index, r.ok
	}
	idx, ok := resolveAndIndex(cfg, libraryID, deadline)
	cache[libraryID] = fileIndexResult{index: idx, ok: ok}
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex done libraryID=%q", libraryID))
	return idx, ok
}

// resolveAndIndex is the uncached variant: resolve the library's mount and
// walk it. Both stages fail-closed by skipping the pair (caller responsibility):
// without a real file index we cannot match songs to files and any read
// attempt would just regress to the s.Path bug.
func resolveAndIndex(cfg pluginConfig, libraryID string, deadline time.Time) (map[string][]fileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndIndex start libraryID=%q", libraryID))
	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot access filesystem for library=%q: %v – skipping pair", libraryID, err))
		return nil, false
	}
	idx, err := buildFileIndexWithoutCache(mountPoint, deadline)
	if time.Now().After(deadline) {
		logTrace(fmt.Sprintf("nd-rating-sync: resolveAndIndex stop, deadline reached libraryID=%q", libraryID))
		return idx, true
	}
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot read library mount %q – skipping pair", mountPoint))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: read mount %q error: %q", mountPoint, err.Error()))
		return nil, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndIndex done libraryID=%q", libraryID))
	logDebug(fmt.Sprintf(
		"nd-rating-sync: indexed mount %q for library=%q – %d size buckets",
		mountPoint, libraryID, len(idx)))
	return idx, true
}

func scanChunk(libraryID string, state *ScanState, deadline time.Time) error {
	logTrace(fmt.Sprintf("nd-rating-sync: scanChunk start lib=%q pending_dirs=%d", libraryID, len(state.PendingDirs)))
	dirty := false
	for len(state.PendingDirs) > 0 {
		if time.Now().After(deadline) {
			logTrace(fmt.Sprintf("nd-rating-sync: scanChunk stop, deadline reached lib=%q pending_dirs=%d", libraryID, len(state.PendingDirs)))
			break
		}

		dir := state.PendingDirs[0]
		state.PendingDirs = state.PendingDirs[1:]
		entries, err := os.ReadDir(dir)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: cannot read directory %q for library=%q (retrying later): %v", dir, libraryID, err))
			state.PendingDirs = append([]string{dir}, state.PendingDirs...)
			logTrace(fmt.Sprintf("nd-rating-sync: scanChunk stop, unreadable directory lib=%q dir=%q", libraryID, dir))
			break
		}
		dirty = true
		logDebug(fmt.Sprintf("nd-rating-sync: scanChunk scanning directory %q for library=%q entries=%d", dir, libraryID, len(entries)))

		updates := map[string]map[string]FileRecord{}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				state.PendingDirs = append(state.PendingDirs, full)
				continue
			}
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
			if !isSupportedExt(ext) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				logDebug(fmt.Sprintf("nd-rating-sync: cannot stat file %q for library=%q (skipping): %v", full, libraryID, err))
				continue
			}

			key := sizeKey(info.Size(), ext)
			bucket, ok := updates[key]
			if !ok {
				bucket = map[string]FileRecord{}
				updates[key] = bucket
			}
			bucket[full] = FileRecord{Path: full, Mtime: info.ModTime().Unix()}
		}

		for key, currentRecords := range updates {
			parts := strings.SplitN(key, ":", 2)
			size, _ := strconv.ParseInt(parts[0], 10, 64)
			ext := parts[1]
			existing, err := loadBucket(libraryID, size, ext)
			if err != nil {
				return err
			}
			merged := mergeBucketRecords(existing, currentRecords, dir)
			if !bucketRecordsEqual(existing, merged) {
				logDebug(fmt.Sprintf("nd-rating-sync: scanChunk saving updated bucket libraryID=%q size=%d ext=%q old=%d new=%d", libraryID, size, ext, len(existing), len(merged)))
				if err := saveBucket(libraryID, size, ext, merged); err != nil {
					return err
				}
			} else {
				logTrace(fmt.Sprintf("nd-rating-sync: scanChunk bucket unchanged libraryID=%q size=%d ext=%q records=%d", libraryID, size, ext, len(existing)))
			}
		}
	}
	if len(state.PendingDirs) == 0 {
		state.Complete = true
		logInfo(fmt.Sprintf("nd-rating-sync: scanChunk complete libraryID=%q", libraryID))
	}
	if !dirty {
		logTrace(fmt.Sprintf("nd-rating-sync: scanChunk no state change libraryID=%q complete=%v pending_dirs=%d", libraryID, state.Complete, len(state.PendingDirs)))
		return nil
	}
	logTrace(fmt.Sprintf("nd-rating-sync: scanChunk save state libraryID=%q complete=%v pending_dirs=%d", libraryID, state.Complete, len(state.PendingDirs)))
	return saveLibraryScanState(libraryID, state)
}

func mergeBucketRecords(existing []FileRecord, currentRecords map[string]FileRecord, dir string) []FileRecord {
	// Normalize the directory path for consistent prefix matching
	prefix := filepath.Clean(dir) + string(os.PathSeparator)
	seen := make(map[string]FileRecord, len(existing)+len(currentRecords))
	for _, r := range existing {
		cleanPath := filepath.Clean(r.Path)
		if strings.HasPrefix(cleanPath, prefix) {
			continue
		}
		seen[r.Path] = r
	}
	for _, r := range currentRecords {
		seen[r.Path] = r
	}
	merged := make([]FileRecord, 0, len(seen))
	for _, r := range seen {
		merged = append(merged, r)
	}
	return merged
}

func bucketRecordsEqual(a, b []FileRecord) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int64{}
	for _, r := range a {
		seen[r.Path] = r.Mtime
	}
	for _, r := range b {
		mtime, ok := seen[r.Path]
		if !ok || mtime != r.Mtime {
			return false
		}
	}
	return true
}
