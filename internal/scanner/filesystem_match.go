package scanner

import (
	"fmt"
	"strings"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
)

func matchFile(index map[string][]FileEntry, s subsonicSong) (FileEntry, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: matchFile start song=%q size=%d suffix=%q", s.ID, s.Size, s.Suffix))
	cands := index[sizeKey(s.Size, strings.ToLower(s.Suffix))]
	if len(cands) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFile stop, ambiguous match song=%q candidates=%d", s.ID, len(cands)))
		return FileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFile done song=%q path=%q", s.ID, cands[0].Path))
	return cands[0], true
}

// matchFileInBucketCache resolves a song through the persistent index,
// loading each bucket from the KV store at most once per callback (cache may
// be nil to skip memoising).
//
// Bucket records keep the mtime in whole seconds while the incremental
// threshold has nanoseconds, so the returned MTime is the END of the stored
// second: a file changed in the same second the last sweep started is then
// re-read instead of being skipped as unchanged.
func matchFileInBucketCache(libraryID string, song subsonicSong, cache map[string][]FileRecord) (FileEntry, bool) {
	ext := strings.ToLower(song.Suffix)
	key := sizeKey(song.Size, ext)
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache start libraryID=%q song=%q size=%d ext=%q", libraryID, song.ID, song.Size, ext))

	records, found := cache[key]
	if found {
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache cache hit libraryID=%q key=%q records=%d", libraryID, key, len(records)))
	} else {
		var err error
		records, err = kvadapter.LoadBucket(libraryID, song.Size, ext)
		if err != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: KV store lookup failed for library=%q size=%d ext=%q: %q", libraryID, song.Size, ext, err.Error()))
			records = nil
		}
		logDebug(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache loaded bucket libraryID=%q key=%q records=%d", libraryID, key, len(records)))
		if cache != nil {
			cache[key] = records
		}
	}
	if len(records) != 1 {
		logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache stop, ambiguous bucket libraryID=%q song=%q key=%q records=%d", libraryID, song.ID, key, len(records)))
		return FileEntry{}, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: matchFileInBucketCache done libraryID=%q song=%q path=%q", libraryID, song.ID, records[0].Path))
	return FileEntry{
		Path:  records[0].Path,
		Size:  song.Size,
		MTime: time.Unix(records[0].Mtime, 0).Add(time.Second - time.Nanosecond),
	}, true
}
