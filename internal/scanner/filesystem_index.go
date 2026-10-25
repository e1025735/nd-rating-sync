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

// errWalkDeadline reports that a mount walk ran out of budget before it
// covered the whole library, so its index must not be used.
var errWalkDeadline = errors.New("deadline reached while walking the library")

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

// buildFileIndexWithoutCache walks the whole mount and indexes its audio
// files by sizeKey. It returns errWalkDeadline when the deadline cut the walk
// short: a partial index would make missing files look like unique matches.
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

// walkAudioFiles calls fn for every supported audio file under root. An
// unreadable subdirectory is logged and skipped; the deadline aborts the whole
// walk with errWalkDeadline.
func walkAudioFiles(root string, deadline time.Time, fn func(path string, size int64, mtime time.Time)) error {
	logTrace(fmt.Sprintf("nd-rating-sync: walkAudioFiles start root=%q", root))
	if time.Now().After(deadline) {
		logTrace(fmt.Sprintf("nd-rating-sync: walkAudioFiles stop, deadline reached root=%q", root))
		return errWalkDeadline
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if e.IsDir() {
			if subErr := walkAudioFiles(full, deadline, fn); subErr != nil {
				if errors.Is(subErr, errWalkDeadline) {
					return subErr
				}
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
	err   error
}

// cachedFileIndex returns the library's file index, walking the mount at most
// once per callback. errWalkDeadline means the walk did not finish; any other
// error means the mount is unusable (already logged).
func cachedFileIndex(cache map[string]fileIndexCacheEntry, libraryID string, deadline time.Time) (map[string][]FileEntry, error) {
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex start libraryID=%q", libraryID))
	if r, found := cache[libraryID]; found {
		logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex done, cache hit libraryID=%q", libraryID))
		return r.index, r.err
	}
	idx, err := resolveAndBuildIndex(libraryID, deadline)
	cache[libraryID] = fileIndexCacheEntry{index: idx, err: err}
	logTrace(fmt.Sprintf("nd-rating-sync: cachedFileIndex done libraryID=%q", libraryID))
	return idx, err
}

// slowWalkWarning is the share of callBudget a mount walk may take before the
// plugin suggests the persistent index: without it every callback walks the
// whole library again and that time is lost for songs.
const slowWalkWarning = callBudget / 3

func resolveAndBuildIndex(libraryID string, deadline time.Time) (map[string][]FileEntry, error) {
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex start libraryID=%q", libraryID))
	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot access filesystem for library=%q: %q – skipping pair", libraryID, err.Error()))
		return nil, err
	}
	start := time.Now()
	idx, err := buildFileIndexWithoutCache(mountPoint, deadline)
	if errors.Is(err, errWalkDeadline) {
		logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex stop, deadline reached libraryID=%q", libraryID))
		return nil, err
	}
	if err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex stop, walk failed libraryID=%q", libraryID))
		logWarn(fmt.Sprintf("nd-rating-sync: cannot read library mount %q – skipping pair", mountPoint))
		logDebug(fmt.Sprintf("nd-rating-sync: read mount %q error: %q", mountPoint, err.Error()))
		return nil, err
	}
	if took := time.Since(start); took > slowWalkWarning {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: walking library=%q took %s of the %s budget and is repeated in every callback – consider enabling cache_libraries_filesystem_tree",
			libraryID, took.Round(time.Millisecond), callBudget))
	}
	logTrace(fmt.Sprintf("nd-rating-sync: resolveAndBuildIndex done libraryID=%q took=%s", libraryID, time.Since(start).Round(time.Millisecond)))
	logDebug(fmt.Sprintf("nd-rating-sync: indexed mount %q for library=%q – %d size buckets", mountPoint, libraryID, len(idx)))
	return idx, nil
}
