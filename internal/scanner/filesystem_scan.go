package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
)

// bucketID names one persistent index bucket of a library.
type bucketID struct {
	size int64
	ext  string
}

// scanLibraryChunk advances a persistent index build until the deadline.
//
// The head of state.PendingDirs is the directory being read. Its entries are
// indexed in name order (os.ReadDir sorts them) and state.ResumeAfter records
// the last one done, so a build cut off by the deadline resumes inside the
// same directory instead of losing the rest of it. The directory is popped
// only once all its entries are done; subdirectories are queued as they are
// met. At least one entry is indexed per directory read, so every call makes
// progress even when reading a directory takes most of the budget.
//
// A new build starts with an empty set of buckets (see ensureLibraryIndexed),
// so records are only ever added: re-indexing part of a directory after a
// resume or a failed state save just rewrites the same records.
func scanLibraryChunk(libraryID string, state *ScanState, deadline time.Time) error {
	logTrace(fmt.Sprintf("nd-rating-sync: scanLibraryChunk start lib=%q pending=%d", libraryID, len(state.PendingDirs)))
	changed := false
	for len(state.PendingDirs) > 0 && !time.Now().After(deadline) {
		dir := state.PendingDirs[0]
		changed = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: cannot read directory %q for library=%q (skipping): %q", dir, libraryID, err.Error()))
			state.PendingDirs = state.PendingDirs[1:]
			state.ResumeAfter = ""
			continue
		}

		start := 0
		if state.ResumeAfter != "" {
			after := state.ResumeAfter
			start = sort.Search(len(entries), func(i int) bool { return entries[i].Name() > after })
			logTrace(fmt.Sprintf("nd-rating-sync: scanLibraryChunk resuming dir=%q after=%q at entry %d of %d", dir, after, start, len(entries)))
		}
		updates := map[bucketID][]FileRecord{}
		finished := true
		for i := start; i < len(entries); i++ {
			if i > start && time.Now().After(deadline) {
				finished = false
				break
			}
			e := entries[i]
			state.ResumeAfter = e.Name()
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
				logWarn(fmt.Sprintf("nd-rating-sync: failed to stat file %q: %q", full, err.Error()))
				continue
			}
			id := bucketID{size: info.Size(), ext: ext}
			updates[id] = append(updates[id], FileRecord{Path: full, Mtime: info.ModTime().Unix()})
		}
		if err := saveBucketUpdates(libraryID, updates); err != nil {
			return err
		}
		if !finished {
			logTrace(fmt.Sprintf("nd-rating-sync: scanLibraryChunk paused dir=%q resume_after=%q buckets_written=%d", dir, state.ResumeAfter, len(updates)))
			break
		}
		logTrace(fmt.Sprintf("nd-rating-sync: scanLibraryChunk finished dir=%q entries=%d buckets=%d pending=%d", dir, len(entries), len(updates), len(state.PendingDirs)-1))
		state.PendingDirs = state.PendingDirs[1:]
		state.ResumeAfter = ""
	}
	if len(state.PendingDirs) == 0 && !state.Complete {
		state.Complete = true
		changed = true
	}
	logTrace(fmt.Sprintf("nd-rating-sync: scanLibraryChunk done lib=%q pending=%d complete=%v changed=%v", libraryID, len(state.PendingDirs), state.Complete, changed))
	if !changed {
		return nil
	}
	return kvadapter.SaveLibraryScanState(libraryID, state)
}

// saveBucketUpdates merges the records found in one directory into their
// buckets, writing only buckets that actually change.
func saveBucketUpdates(libraryID string, updates map[bucketID][]FileRecord) error {
	for id, records := range updates {
		existing, err := kvadapter.LoadBucket(libraryID, id.size, id.ext)
		if err != nil {
			return err
		}
		merged := mergeBucketRecords(existing, records)
		if bucketRecordsEqual(existing, merged) {
			logTrace(fmt.Sprintf("nd-rating-sync: saveBucketUpdates unchanged lib=%q size=%d ext=%q records=%d", libraryID, id.size, id.ext, len(merged)))
			continue
		}
		logTrace(fmt.Sprintf("nd-rating-sync: saveBucketUpdates writing lib=%q size=%d ext=%q records=%d", libraryID, id.size, id.ext, len(merged)))
		if err := kvadapter.SaveBucket(libraryID, id.size, id.ext, merged); err != nil {
			return err
		}
	}
	return nil
}

// mergeBucketRecords adds records to existing, replacing any record with the
// same path. Order is kept: existing records first, then new ones.
func mergeBucketRecords(existing, records []FileRecord) []FileRecord {
	merged := make([]FileRecord, 0, len(existing)+len(records))
	at := make(map[string]int, len(existing)+len(records))
	add := func(r FileRecord) {
		if i, ok := at[r.Path]; ok {
			merged[i] = r
			return
		}
		at[r.Path] = len(merged)
		merged = append(merged, r)
	}
	for _, r := range existing {
		add(r)
	}
	for _, r := range records {
		add(r)
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
