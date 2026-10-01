package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)


// resolveMountPoint maps a configured library ID to its in-sandbox mount point.
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

func isSupportedExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4", "wma":
		return true
	}
	return false
}

func sizeKey(size int64, ext string) string {
	return strconv.FormatInt(size, 10) + ":" + ext
}

func buildFileIndexWithoutCache(mountPoint string, deadline time.Time) (map[string][]FileEntry, error) {
	logTrace(fmt.Sprintf("nd-rating-sync: buildFileIndexWithoutCache start mountPoint=%q", mountPoint))
	index := map[string][]FileEntry{}
	err := walkAudioFiles(mountPoint, deadline, func(path string, size int64, mtime time.Time) {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		k := sizeKey(size, ext)
		index[k] = append(index[k], FileEntry{Path: path, Size: size, MTime: mtime})
	})
	if err != nil {
		return nil, err
	}
	logTrace(fmt.Sprintf("nd-rating-sync: buildFileIndexWithoutCache done mountPoint=%q", mountPoint))
	return index, nil
}

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

type fileIndexResult struct {
	index map[string][]FileEntry
	ok    bool
}

func cachedFileIndex(cache map[string]fileIndexResult, libraryID string, deadline time.Time) (map[string][]FileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex start libraryID=%q", libraryID))
	if r, found := cache[libraryID]; found {
		return r.index, r.ok
	}
	idx, ok := resolveAndIndex(libraryID, deadline)
	cache[libraryID] = fileIndexResult{index: idx, ok: ok}
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex done libraryID=%q", libraryID))
	return idx, ok
}

func resolveAndIndex(libraryID string, deadline time.Time) (map[string][]FileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndIndex start libraryID=%q", libraryID))
	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot access filesystem for library=%q: %v – skipping pair", libraryID, err))
		return nil, false
	}
	idx, err := buildFileIndexWithoutCache(mountPoint, deadline)
	if time.Now().After(deadline) {
		logTrace(fmt.Sprintf("nd-rating-sync: resolveAndIndex stop, deadline reached libraryID=%q", libraryID))
		return idx, true
	}
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot read library mount %q – skipping pair", mountPoint))
		logDebug(fmt.Sprintf("nd-rating-sync: read mount %q error: %q", mountPoint, err.Error()))
		return nil, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndIndex done libraryID=%q", libraryID))
	logDebug(fmt.Sprintf("nd-rating-sync: indexed mount %q for library=%q – %d size buckets", mountPoint, libraryID, len(idx)))
	return idx, true
}

func matchFile(index map[string][]FileEntry, s subsonicSong) (FileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: matchFile start song=%q", s.ID))
	cands := index[sizeKey(s.Size, strings.ToLower(s.Suffix))]
	if len(cands) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFile stop, ambiguous match song=%q", s.ID))
		return FileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFile done song=%q", s.ID))
	return cands[0], true
}

func matchFileFromBucketCache(libraryID string, song subsonicSong, cache map[string][]FileRecord) (FileEntry, bool) {
	ext := strings.ToLower(song.Suffix)
	key := sizeKey(song.Size, ext)
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache start libraryID=%q song=%q size=%d ext=%q", libraryID, song.ID, song.Size, ext))
	if cache == nil {
		records, err := kvadapter.LoadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %v", libraryID, song.Size, ext, err))
			return FileEntry{}, false
		}
		if len(records) != 1 {
			logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache stop, ambiguous bucket libraryID=%q song=%q size=%d ext=%q records=%d", libraryID, song.ID, song.Size, ext, len(records)))
			return FileEntry{}, false
		}
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache done libraryID=%q song=%q path=%q", libraryID, song.ID, records[0].Path))
		return FileEntry{Path: records[0].Path, Size: song.Size, MTime: time.Unix(records[0].Mtime, 0)}, true
	}

	records, found := cache[key]
	if found {
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache cache hit libraryID=%q key=%q records=%d", libraryID, key, len(records)))
	} else {
		var err error
		records, err = kvadapter.LoadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %v", libraryID, song.Size, ext, err))
			cache[key] = nil
			return FileEntry{}, false
		}
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache loaded bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		cache[key] = records
	}
	if len(records) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache stop, ambiguous bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		return FileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileFromBucketCache done libraryID=%q key=%q path=%q", libraryID, key, records[0].Path))
	return FileEntry{Path: records[0].Path, Size: song.Size, MTime: time.Unix(records[0].Mtime, 0)}, true
}

func scanChunk(libraryID string, state *ScanState, deadline time.Time) error {
	logTrace(fmt.Sprintf("scanChunk start lib=%q pending=%d", libraryID, len(state.PendingDirs)))
	if state.VisitedDirs == nil {
		state.VisitedDirs = make(map[string]struct{})
	}
	changed := false
	mark := func() { changed = true }
	for len(state.PendingDirs) > 0 {
		if time.Now().After(deadline) {
			break
		}
		dir, ok := popDir(state)
		if !ok {
			break
		}
		mark()
		if _, seen := state.VisitedDirs[dir]; seen {
			continue
		}
		markVisited(state, dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: cannot read directory %q for library=%q: %v", dir, libraryID, err))
			requeueDir(state, dir)
			continue
		}
		updates := map[string]map[string]FileRecord{}
		for _, e := range entries {
			if time.Now().After(deadline) {
				requeueDir(state, dir)
				mark()
				continue
			}
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				if _, seen := state.VisitedDirs[full]; !seen {
					requeueDir(state, full)
				}
				continue
			}
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
			if !isSupportedExt(ext) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				logWarn(fmt.Sprintf("failed to stat file %q: %v", full, err))
				continue
			}
			key := sizeKey(info.Size(), ext)
			bucket := updates[key]
			if bucket == nil {
				bucket = map[string]FileRecord{}
				updates[key] = bucket
			}
			bucket[full] = FileRecord{Path: full, Mtime: info.ModTime().Unix()}
		}
		for key, current := range updates {
			parts := strings.SplitN(key, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid bucket key %q", key)
			}
			size, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid size in bucket key %q: %w", key, err)
			}
			ext := parts[1]
			existing, err := kvadapter.LoadBucket(libraryID, size, ext)
			if err != nil {
				return err
			}
			merged := mergeBucketRecords(existing, current, dir)
			if !bucketRecordsEqual(existing, merged) {
				if err := kvadapter.SaveBucket(libraryID, size, ext, merged); err != nil {
					return err
				}
				mark()
			}
		}
	}
	if len(state.PendingDirs) == 0 {
		state.Complete = true
		mark()
	}
	if !changed {
		return nil
	}
	return kvadapter.SaveLibraryScanState(libraryID, state)
}

func popDir(state *ScanState) (string, bool) {
	if len(state.PendingDirs) == 0 {
		return "", false
	}
	dir := state.PendingDirs[0]
	state.PendingDirs = state.PendingDirs[1:]
	return dir, true
}

func requeueDir(state *ScanState, dir string) {
	state.PendingDirs = append(state.PendingDirs, dir)
}

func markVisited(state *ScanState, dir string) bool {
	if state.VisitedDirs == nil {
		state.VisitedDirs = make(map[string]struct{})
	}
	if _, seen := state.VisitedDirs[dir]; seen {
		return false
	}
	state.VisitedDirs[dir] = struct{}{}
	return true
}

func mergeBucketRecords(existing []FileRecord, currentRecords map[string]FileRecord, dir string) []FileRecord {
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
