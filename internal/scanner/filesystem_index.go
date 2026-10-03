package scanner

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

type fileIndexCacheEntry struct {
	index map[string][]FileEntry
	ok    bool
}

func cachedFileIndex(cache map[string]fileIndexCacheEntry, libraryID string, deadline time.Time) (map[string][]FileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex start libraryID=%q", libraryID))
	if r, found := cache[libraryID]; found {
		return r.index, r.ok
	}
	idx, ok := resolveAndBuildIndex(libraryID, deadline)
	cache[libraryID] = fileIndexCacheEntry{index: idx, ok: ok}
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex done libraryID=%q", libraryID))
	return idx, ok
}

func resolveAndBuildIndex(libraryID string, deadline time.Time) (map[string][]FileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex start libraryID=%q", libraryID))
	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot access filesystem for library=%q: %v – skipping pair", libraryID, err))
		return nil, false
	}
	idx, err := buildFileIndexWithoutCache(mountPoint, deadline)
	if time.Now().After(deadline) {
		logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex stop, deadline reached libraryID=%q", libraryID))
		return idx, true
	}
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot read library mount %q – skipping pair", mountPoint))
		logDebug(fmt.Sprintf("nd-rating-sync: read mount %q error: %q", mountPoint, err.Error()))
		return nil, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex done libraryID=%q", libraryID))
	logDebug(fmt.Sprintf("nd-rating-sync: indexed mount %q for library=%q – %d size buckets", mountPoint, libraryID, len(idx)))
	return idx, true
}
