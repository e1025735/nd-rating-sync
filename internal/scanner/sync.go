package scanner

import (
	"fmt"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
)

const (
	songPageSize       = subsonicadapter.SongPageSize
	deadlineCheckEvery = 1
)

// runSyncChunk processes as many songs as fit before deadline, starting from
// cur, and returns the position to resume at plus whether the whole sweep is
// finished. It walks (library, user) pairs in order: for each fresh pair it
// stamps PairStart (the eventual incremental threshold) and, once the pair is
// fully processed, persists that threshold. When the deadline is reached
// mid-sweep it returns allDone=false with a cursor the caller hands to a
// continuation callback.
//
// Forward progress is guaranteed: the budget is only checked at pair
// boundaries here and after each song in processPairChunk, so every invocation
// either advances the cursor or completes the sweep – a chain of continuations
// always terminates.
func runSyncChunk(cfg PluginConfig, cur SyncCursor, deadline time.Time) (SyncCursor, bool) {
	// Per-call caches: LibraryGetLibrary results (for the LastScanAt gate),
	// file-index results (for size-based file matching), and persistent index
	// bucket lookups. Globals do not persist across callbacks, so all are
	// intentionally scoped to one invocation.
	libCache := map[string]libScanResult{}
	indexCache := map[string]fileIndexCacheEntry{}
	bucketCache := map[string][]FileRecord{}

	logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk start lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
	for {
		// Skip exhausted users/libraries. Also tolerates indices that point
		// past the end after a config change between continuations.
		for cur.Lib < len(cfg.Libraries) && cur.User >= len(cfg.Libraries[cur.Lib].Users) {
			cur.Lib++
			cur.User = 0
			cur.Offset = 0
			cur.PairStart = ""
		}
		if cur.Lib >= len(cfg.Libraries) {
			kvStorageUsage, err := kvadapter.GetPercentageKVStorageUsage(cfg.KVStorageMaxSize)
			if err != nil {
				logWarn("nd-rating-sync: kv storage usage could not be fetched")
				logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, sweep complete lib=%q user=%q, offset=%q, deadline=%q",
					cur.Lib, cur.User, cur.Offset, deadline))
				return cur, true
			}
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, sweep complete lib=%q user=%q, offset=%q, deadline=%q, kvStorageUsage=%.2f%%",
				cur.Lib, cur.User, cur.Offset, deadline, kvStorageUsage))
			return cur, true // whole sweep complete
		}
		// A valid pair is selected. If the budget is gone, resume here.
		if time.Now().After(deadline) {
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline reached lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			return cur, false
		}

		lib := cfg.Libraries[cur.Lib]
		u := lib.Users[cur.User]
		logDebug(fmt.Sprintf("nd-rating-sync: will look for the following ratings: %v", u.RatingTagOrder))
		if u.Username == "" {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: skipping library=%q user#%d – empty username in config", lib.LibraryID, cur.User))
			cur.User++
			cur.Offset = 0
			cur.PairStart = ""
			continue
		}

		// Load the incremental threshold once for this pair; reused by the
		// LastScanAt gate below and passed into processPairChunk for the
		// per-file mtime skip.
		var threshold time.Time
		if cfg.IncrementalSync {
			threshold = kvadapter.LoadLastSynced(lib.LibraryID, u.Username)
		}

		// LastScanAt gate: when starting a fresh pair, skip the whole pair if
		// Navidrome has not rescanned the library since our last completed sweep
		// — no song paging at all. A gated skip must NOT save the threshold:
		// nothing was processed, so it stays pinned to the last real sweep and a
		// later scan still re-processes the files that changed in it.
		if cur.Offset == 0 && cur.PairStart == "" && cfg.IncrementalSync && !threshold.IsZero() {
			if scanned, ok := cachedLibraryLastScan(libCache, lib.LibraryID); ok && scanned.Before(threshold) {
				logInfo(fmt.Sprintf(
					"nd-rating-sync: skipping user=%q library=%q – library unchanged since last sync (last_scan=%s threshold=%s)",
					u.Username, lib.LibraryID, scanned.UTC().Format(time.RFC3339), threshold.UTC().Format(time.RFC3339)))
				cur.User++
				cur.Offset = 0
				cur.PairStart = ""
				continue
			}
		}

		// Resolve the library's sandbox mount and index its files by (size,
		// suffix). Navidrome does not give plugins an openable path for a song
		// (the Subsonic `path` is a synthesized fake by default), so the only
		// way to locate a file is to walk the mount the host provides. A
		// failure here is non-fatal and the pair is skipped WITHOUT saving the
		// threshold – nothing was processed, so the next run retries from the
		// same baseline.
		index := map[string][]FileEntry{}
		usePersistentIndex := cfg.CacheLibrariesFilesystemTree

		if usePersistentIndex {
			ready, err := ensureLibraryIndexed(lib.LibraryID, deadline)

			if err != nil {
				logWarn(fmt.Sprintf(
					"nd-rating-sync: skipping library=%q user#%d – library threw an error", lib.LibraryID, cur.User))
				cur.User++
				cur.Offset = 0
				cur.PairStart = ""
				continue
			}

			if !ready {
				kvStorageUsage, err := kvadapter.GetPercentageKVStorageUsage(cfg.KVStorageMaxSize)
				if err != nil {
					logDebug("nd-rating-sync: kv storage usage could not be fetched")
					logTrace(fmt.Sprintf(
						"nd-rating-sync: runSyncChunk stop, deadline reached with cache lib=%q user=%q, offset=%q, deadline=%q",
						cur.Lib, cur.User, cur.Offset, deadline))
					return cur, false
				}
				logTrace(fmt.Sprintf(
					"nd-rating-sync: runSyncChunk stop, deadline reached with cache lib=%q user=%q, offset=%q, deadline=%q, kvStorageUsage=%.2f%%",
					cur.Lib, cur.User, cur.Offset, deadline, kvStorageUsage))
				return cur, false
			}
		} else {
			indexTmp, indexOK := cachedFileIndex(indexCache, lib.LibraryID, deadline)
			index = indexTmp
			if time.Now().After(deadline) {
				logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit without cache lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
				return cur, false
			}
			if !indexOK {
				cur.User++
				cur.Offset = 0
				cur.PairStart = ""
				continue
			}
		}

		if cur.PairStart == "" {
			cur.PairStart = time.Now().UTC().Format(time.RFC3339Nano)
		}

		next, pairDone := processPairChunk(lib, u, cfg, cur, threshold, deadline, index, usePersistentIndex, bucketCache)
		cur = next
		if !pairDone {
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit in processPairChunk lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			return cur, false // deadline hit (or fetch failed) mid-pair
		}

		// Pair finished: persist the threshold captured when the pair started.
		if cfg.IncrementalSync && !cfg.DryRun {
			if ps, err := time.Parse(time.RFC3339Nano, cur.PairStart); err == nil {
				kvadapter.SaveLastSynced(lib.LibraryID, u.Username, ps)
			}
		}

		cur.User++
		cur.Offset = 0
		cur.PairStart = ""
	}
}

func ensureLibraryIndexed(libraryID string, deadline time.Time) (bool, error) {
	logTrace(fmt.Sprintf("nd-rating-sync: ensureLibraryIndexed start libraryID=%q", libraryID))
	state, err := kvadapter.LoadLibraryScanState(libraryID)
	if err != nil {
		return false, err
	}
	logDebug(fmt.Sprintf(
		"nd-rating-sync: ensureLibraryIndexed loaded state libraryID=%q complete=%v pending_dirs=%d last_scan_at=%d",
		libraryID, state.Complete, len(state.PendingDirs), state.LastScanAt))

	currentLastScan, lastScanOK := libraryLastScan(libraryID)
	if lastScanOK {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: ensureLibraryIndexed current libraryLastScan libraryID=%q lastScan=%s",
			libraryID, currentLastScan.UTC().Format(time.RFC3339Nano)))
	}

	if state.Complete && len(state.PendingDirs) == 0 {
		if !lastScanOK || state.LastScanAt >= currentLastScan.Unix() {
			logTrace(fmt.Sprintf("nd-rating-sync: ensureLibraryIndexed cached index still valid libraryID=%q", libraryID))
			return true, nil
		}
	}

	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		return false, err
	}

	if lastScanOK && state.LastScanAt != 0 && currentLastScan.Unix() > state.LastScanAt {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: ensureLibraryIndexed stale index libraryID=%q lastScan=%s saved=%s, restarting scan",
			libraryID, currentLastScan.UTC().Format(time.RFC3339Nano), time.Unix(state.LastScanAt, 0).UTC().Format(time.RFC3339Nano)))
		state = ScanState{PendingDirs: []string{mountPoint}, LastScanAt: currentLastScan.Unix()}
	}

	if len(state.PendingDirs) == 0 {
		state.PendingDirs = []string{mountPoint}
		logDebug(fmt.Sprintf("nd-rating-sync: ensureLibraryIndexed initializing pending_dirs for libraryID=%q mountPoint=%q", libraryID, mountPoint))
		if !state.Complete && lastScanOK {
			state.LastScanAt = currentLastScan.Unix()
		}
	}

	for !time.Now().After(deadline) {
		if err := scanLibraryChunk(libraryID, &state, deadline); err != nil {
			return false, err
		}

		if state.Complete {
			logInfo(fmt.Sprintf("nd-rating-sync: ensureLibraryIndexed complete libraryID=%q", libraryID))
			return true, nil
		}
		logTrace(fmt.Sprintf(
			"nd-rating-sync: ensureLibraryIndexed paused libraryID=%q pending_dirs=%d deadline=%s",
			libraryID, len(state.PendingDirs), deadline))
	}

	logTrace(fmt.Sprintf("nd-rating-sync: ensureLibraryIndexed stop libraryID=%q deadline reached pending_dirs=%d", libraryID, len(state.PendingDirs)))
	return false, nil
}

// processPairChunk processes songs for a single (library, user) pair starting
// at cur.Offset until the deadline elapses or the pair's songs are exhausted.
// It returns the advanced cursor and whether the pair is complete. The deadline
// is checked after each processed song, so at least one song is handled per
// call (provided the deadline had not already passed on entry, which
// runSyncChunk guarantees) – this is what makes the continuation chain
// terminate.
//
// A page-fetch failure returns pairDone=false without advancing past the failed
// page, so the next run retries the same offset; the cursor already points at
// the first unprocessed song.
func processPairChunk(lib LibraryConfig, u UserConfig, cfg PluginConfig, cur SyncCursor, threshold time.Time, deadline time.Time, index map[string][]FileEntry, usePersistentIndex bool, bucketCache map[string][]FileRecord) (SyncCursor, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk start lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
	if cfg.DryRun {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] user=%q – no ratings will be written", u.Username))
	}
	if u.ClearRatingIfUntagged && u.SkipAlreadyRated {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: user=%q has clear_rating_if_untagged=true but skip_already_rated=true – "+
				"songs already rated in Navidrome will be skipped before the file is read and their ratings will NOT be cleared",
			u.Username))
	}
	logInfo(fmt.Sprintf(
		"nd-rating-sync: syncing user=%q library=%q from offset=%d skip_already_rated=%v clear_rating_if_untagged=%v dry_run=%v tag_order=%q incremental_threshold=%s",
		u.Username, lib.LibraryID, cur.Offset, u.SkipAlreadyRated, u.ClearRatingIfUntagged, cfg.DryRun, u.RatingTagOrder, formatThreshold(threshold)))

	var tally syncTally
	for {
		pageOffset := (cur.Offset / songPageSize) * songPageSize
		skip := cur.Offset - pageOffset

		page, more, err := subsonicadapter.FetchSongPage(u.Username, lib.LibraryID, pageOffset, songPageSize)
		if err != nil {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: fetching songs for user=%q library=%q at offset=%d failed: %q – will retry next run",
				u.Username, lib.LibraryID, pageOffset, err.Error()))
			tally.log(u.Username, lib.LibraryID, cfg.DryRun)
			return cur, false
		}

		// Resume position is at/past the end of this page (only happens when a
		// page came back shorter than expected). Advance or finish.
		if skip >= len(page) {
			if !more {
				logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, no more lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
				tally.log(u.Username, lib.LibraryID, cfg.DryRun)
				return cur, true
			}
			cur.Offset = pageOffset + songPageSize
			continue
		}

		for i := skip; i < len(page); i++ {
			processSong(u, cfg, page[i], threshold, index, usePersistentIndex, lib.LibraryID, bucketCache, &tally)
			cur.Offset = pageOffset + i + 1
			// Check the deadline only every deadlineCheckEvery songs so the
			// hot loop does not hammer the WASI clock import. Reducing the
			// rate also reduces the surface area for the host-side clock
			// panic we have seen in production (see callBudget docs).
			if (i-skip+1)%deadlineCheckEvery == 0 && time.Now().After(deadline) {
				logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, deadline reached lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
				tally.log(u.Username, lib.LibraryID, cfg.DryRun)
				return cur, false
			}
		}

		if !more {
			logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk done lib=%q user=%q, offset=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			tally.log(u.Username, lib.LibraryID, cfg.DryRun)
			return cur, true
		}
		// Page was full; cur.Offset is already at the next page boundary.
	}
}

// syncTally accumulates per-chunk outcome counts for a single summary log line.
type syncTally struct {
	rated, cleared, wouldRate, wouldClear        int
	skippedRated, skippedNoTag, skippedUnchanged int
	skippedUnreadable, errored                   int
}

func (t syncTally) log(username, libraryID string, dryRun bool) {
	if dryRun {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] chunk done user=%q library=%q – would_rate=%d would_clear=%d skipped_already_rated=%d skipped_unchanged=%d skipped_no_tag=%d skipped_unreadable=%d",
			username, libraryID, t.wouldRate, t.wouldClear, t.skippedRated, t.skippedUnchanged, t.skippedNoTag, t.skippedUnreadable))
		return
	}
	logInfo(fmt.Sprintf(
		"nd-rating-sync: chunk done user=%q library=%q – rated=%d cleared=%d skipped_already_rated=%d skipped_unchanged=%d skipped_no_tag=%d skipped_unreadable=%d errors=%d",
		username, libraryID, t.rated, t.cleared, t.skippedRated, t.skippedUnchanged, t.skippedNoTag, t.skippedUnreadable, t.errored))
}

// formatThreshold renders a threshold timestamp for log output, with a
// distinct marker when no threshold has been recorded yet.
func formatThreshold(t time.Time) string {
	if t.IsZero() {
		return "(none – full scan)"
	}
	return t.UTC().Format(time.RFC3339)
}
