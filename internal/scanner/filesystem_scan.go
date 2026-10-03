package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
)

func scanLibraryChunk(libraryID string, state *ScanState, deadline time.Time) error {
	logTrace(fmt.Sprintf("scanLibraryChunk start lib=%q pending=%d", libraryID, len(state.PendingDirs)))
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
	seen := make(map[string]int64, len(a))
	for _, r := range a {
		if _, exists := seen[r.Path]; exists {
			return false
		}
		seen[r.Path] = r.Mtime
	}
	for _, r := range b {
		mtime, ok := seen[r.Path]
		if !ok || mtime != r.Mtime {
			return false
		}
		delete(seen, r.Path)
	}
	return len(seen) == 0
}
