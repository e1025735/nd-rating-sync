package scanner

import (
	"fmt"
	"strings"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
)

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

func matchFileInBucketCache(libraryID string, song subsonicSong, cache map[string][]FileRecord) (FileEntry, bool) {
	ext := strings.ToLower(song.Suffix)
	key := sizeKey(song.Size, ext)
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache start libraryID=%q song=%q size=%d ext=%q", libraryID, song.ID, song.Size, ext))
	if cache == nil {
		records, err := kvadapter.LoadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %v", libraryID, song.Size, ext, err))
			return FileEntry{}, false
		}
		if len(records) != 1 {
			logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache stop, ambiguous bucket libraryID=%q song=%q size=%d ext=%q records=%d", libraryID, song.ID, song.Size, ext, len(records)))
			return FileEntry{}, false
		}
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache done libraryID=%q song=%q path=%q", libraryID, song.ID, records[0].Path))
		return FileEntry{Path: records[0].Path, Size: song.Size, MTime: time.Unix(records[0].Mtime, 0)}, true
	}

	records, found := cache[key]
	if found {
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache cache hit libraryID=%q key=%q records=%d", libraryID, key, len(records)))
	} else {
		var err error
		records, err = kvadapter.LoadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %v", libraryID, song.Size, ext, err))
			cache[key] = nil
			return FileEntry{}, false
		}
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache loaded bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		cache[key] = records
	}
	if len(records) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache stop, ambiguous bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		return FileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache done libraryID=%q key=%q path=%q", libraryID, key, records[0].Path))
	return FileEntry{Path: records[0].Path, Size: song.Size, MTime: time.Unix(records[0].Mtime, 0)}, true
}
