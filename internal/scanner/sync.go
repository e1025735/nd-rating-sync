package scanner

import (
	"errors"
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
// Forward progress is guaranteed: every invocation either processes at least
// one song, advances a persistent index build, or counts a failed attempt in
// cur.Retry, and a pair is skipped after maxPairAttempts failed attempts – so
// a chain of continuations always terminates.
func runSyncChunk(cfg PluginConfig, cur SyncCursor, deadline time.Time) (SyncCursor, bool) {
	return runSyncChunkWithIndexCache(cfg, cur, deadline, map[string]fileIndexCacheEntry{})
}

// runSyncChunkWithIndexCache is runSyncChunk with the per-call file-index
// cache passed in, so tests can stand in for a mount walk.
func runSyncChunkWithIndexCache(cfg PluginConfig, cur SyncCursor, deadline time.Time, indexCache map[string]fileIndexCacheEntry) (SyncCursor, bool) {
	// Per-call caches: LibraryGetLibrary results (for the LastScanAt gate),
	// file-index results (for size-based file matching), and persistent index
	// bucket lookups. Globals do not persist across callbacks, so all are
	// intentionally scoped to one invocation.
	libCache := map[string]libScanResult{}
	bucketCache := map[string][]FileRecord{}
	// worked records that this callback already spent time on a pair, so a
	// mount walk that later runs out of budget is not the walk's fault.
	worked := false

	logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk start lib=%d user=%d offset=%d deadline=%s",
		cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
	for {
		// Skip exhausted users/libraries. Also tolerates indices that point
		// past the end after a config change between continuations.
		for cur.Lib < len(cfg.Libraries) && cur.User >= len(cfg.Libraries[cur.Lib].Users) {
			cur.Lib++
			cur.User = 0
			cur.Offset = 0
			cur.PairStart = ""
			cur.Retry = 0
		}
		if cur.Lib >= len(cfg.Libraries) {
			logKVUsage(cfg)
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, sweep complete lib=%d user=%d offset=%d deadline=%s",
				cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
			return cur, true // whole sweep complete
		}
		// A valid pair is selected. If the budget is gone, resume here.
		if time.Now().After(deadline) {
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline reached lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
			return cur, false
		}

		lib := cfg.Libraries[cur.Lib]
		u := lib.Users[cur.User]
		logDebug(fmt.Sprintf("nd-rating-sync: will look for the following ratings: %q", u.RatingTagOrder))
		if u.Username == "" {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: skipping library=%q user#%d – empty username in config", lib.LibraryID, cur.User))
			cur.nextPair()
			continue
		}

		// Load the incremental threshold once for this pair; reused by the
		// LastScanAt gate below and passed into processPairChunk for the
		// per-file mtime skip. A threshold saved with other pair settings
		// reads as zero (full scan).
		var threshold time.Time
		if cfg.IncrementalSync {
			threshold = kvadapter.LoadLastSynced(lib.LibraryID, u.Username, pairFingerprint(u))
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
				cur.nextPair()
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
		var index map[string][]FileEntry
		usePersistentIndex := cfg.CacheLibrariesFilesystemTree

		if usePersistentIndex {
			ready, err := ensureLibraryIndexed(lib.LibraryID, deadline)
			if err != nil {
				logWarn(fmt.Sprintf(
					"nd-rating-sync: skipping library=%q user#%d – file index failed: %q", lib.LibraryID, cur.User, err.Error()))
				cur.nextPair()
				continue
			}
			if !ready {
				// The build's progress is saved in the KV store; the next
				// callback continues it.
				logKVUsage(cfg)
				logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline reached with cache lib=%d user=%d offset=%d library=%q deadline=%s",
					cur.Lib, cur.User, cur.Offset, lib.LibraryID, deadline.Format(time.RFC3339Nano)))
				return cur, false
			}
		} else {
			idx, err := cachedFileIndex(indexCache, lib.LibraryID, deadline)
			switch {
			case errors.Is(err, errWalkDeadline):
				if worked {
					// Earlier pairs used up this budget; a fresh callback
					// walks with a whole one.
					logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit without cache after earlier pairs lib=%d user=%d offset=%d deadline=%s",
						cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
					return cur, false
				}
				cur.Retry++
				if cur.Retry >= maxPairAttempts {
					logWarn(fmt.Sprintf(
						"nd-rating-sync: skipping user=%q library=%q – walking the library did not finish within %s in %d attempts; enable cache_libraries_filesystem_tree",
						u.Username, lib.LibraryID, callBudget, cur.Retry))
					cur.nextPair()
					continue
				}
				logWarn(fmt.Sprintf(
					"nd-rating-sync: walking library=%q did not finish within %s (attempt %d of %d) – retrying in %ds",
					lib.LibraryID, callBudget, cur.Retry, maxPairAttempts, retryDelaySeconds))
				logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit without cache lib=%d user=%d offset=%d retry=%d deadline=%s",
					cur.Lib, cur.User, cur.Offset, cur.Retry, deadline.Format(time.RFC3339Nano)))
				return cur, false
			case err != nil:
				logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk skipping pair, no file index lib=%d user=%d library=%q", cur.Lib, cur.User, lib.LibraryID))
				cur.nextPair()
				continue
			}
			index = idx
		}
		worked = true

		if cur.PairStart == "" {
			cur.PairStart = time.Now().UTC().Format(time.RFC3339Nano)
		}

		next, pairDone := processPairChunk(lib, u, cfg, cur, threshold, deadline, index, usePersistentIndex, bucketCache)
		cur = next
		if !pairDone {
			if cur.Retry >= maxPairAttempts {
				logWarn(fmt.Sprintf(
					"nd-rating-sync: skipping user=%q library=%q until the next sweep – listing its songs failed %d times in a row",
					u.Username, lib.LibraryID, cur.Retry))
				cur.nextPair()
				continue
			}
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit in processPairChunk lib=%d user=%d offset=%d retry=%d deadline=%s", cur.Lib, cur.User, cur.Offset, cur.Retry, deadline.Format(time.RFC3339Nano)))
			return cur, false // deadline hit (or fetch failed) mid-pair
		}

		// Pair finished: persist the threshold captured when the pair started.
		if cfg.IncrementalSync && !cfg.DryRun {
			if ps, err := time.Parse(time.RFC3339Nano, cur.PairStart); err == nil {
				kvadapter.SaveLastSynced(lib.LibraryID, u.Username, ps, pairFingerprint(u))
			}
		}

		cur.nextPair()
	}
}

// kvUsageWarnPercent is the persistent-index KV usage that triggers a warning.
const kvUsageWarnPercent = 90

// logKVUsage reports how full the plugin's KV store is and, when the
// persistent file index (the only large KV user) is on, warns before the host
// starts rejecting writes.
func logKVUsage(cfg PluginConfig) {
	usage, err := kvadapter.GetPercentageKVStorageUsage(cfg.KVStorageMaxSize)
	if err != nil {
		logDebug(fmt.Sprintf("nd-rating-sync: KV storage usage could not be determined: %q", err.Error()))
		return
	}
	logTrace(fmt.Sprintf("nd-rating-sync: kvStorageUsage=%.2f%%", usage))
	logDebug(fmt.Sprintf("nd-rating-sync: KV storage usage %.1f%% of %s", usage, cfg.KVStorageMaxSize))
	if cfg.CacheLibrariesFilesystemTree && usage >= kvUsageWarnPercent {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KV storage is %.1f%% full – raise permissions.kvstore.maxSize in manifest.json and kv_storage_max_size together",
			usage))
	}
}

// ensureLibraryIndexed advances the persistent file index of a library and
// reports whether it is complete. A complete index is reused until Navidrome
// rescans the library; a rescan (also one in the middle of a build) starts a
// new build, and every new build first deletes the library's old buckets so
// records of moved or deleted files cannot linger in it.
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

	rebuild := false
	switch {
	case state.Complete:
		if !lastScanOK || state.LastScanAt >= currentLastScan.Unix() {
			logTrace(fmt.Sprintf("nd-rating-sync: ensureLibraryIndexed cached index still valid libraryID=%q", libraryID))
			return true, nil
		}
		logInfo(fmt.Sprintf(
			"nd-rating-sync: library=%q was rescanned at %s – rebuilding the file index",
			libraryID, currentLastScan.UTC().Format(time.RFC3339)))
		rebuild = true
	case len(state.PendingDirs) > 0:
		if lastScanOK && state.LastScanAt != 0 && currentLastScan.Unix() > state.LastScanAt {
			logInfo(fmt.Sprintf(
				"nd-rating-sync: library=%q was rescanned during the file index build – starting it again", libraryID))
			rebuild = true
		}
	default:
		rebuild = true
	}

	if rebuild {
		mountPoint, err := resolveMountPoint(libraryID)
		if err != nil {
			return false, err
		}
		deleted, err := kvadapter.DeleteLibraryBuckets(libraryID)
		if err != nil {
			return false, err
		}
		logDebug(fmt.Sprintf("nd-rating-sync: starting file index build libraryID=%q – deleted %d old buckets", libraryID, deleted))
		state = ScanState{PendingDirs: []string{mountPoint}}
		if lastScanOK {
			state.LastScanAt = currentLastScan.Unix()
		}
	}

	for !time.Now().After(deadline) {
		if err := scanLibraryChunk(libraryID, &state, deadline); err != nil {
			return false, err
		}
		if state.Complete {
			logInfo(fmt.Sprintf("nd-rating-sync: file index complete libraryID=%q", libraryID))
			return true, nil
		}
		logTrace(fmt.Sprintf(
			"nd-rating-sync: ensureLibraryIndexed paused libraryID=%q pending_dirs=%d resume_after=%q deadline=%s",
			libraryID, len(state.PendingDirs), state.ResumeAfter, deadline.Format(time.RFC3339Nano)))
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
// page and counts the failure in cur.Retry, so the continuation retries the
// same offset after a delay and runSyncChunk gives up on the pair after
// maxPairAttempts. Every processed song resets cur.Retry.
func processPairChunk(lib LibraryConfig, u UserConfig, cfg PluginConfig, cur SyncCursor, threshold time.Time, deadline time.Time, index map[string][]FileEntry, usePersistentIndex bool, bucketCache map[string][]FileRecord) (SyncCursor, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk start lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
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
			cur.Retry++
			logWarn(fmt.Sprintf(
				"nd-rating-sync: fetching songs for user=%q library=%q at offset=%d failed (attempt %d of %d): %q",
				u.Username, lib.LibraryID, pageOffset, cur.Retry, maxPairAttempts, err.Error()))
			tally.log(u.Username, lib.LibraryID, cfg.DryRun)
			return cur, false
		}

		// Resume position is at/past the end of this page (only happens when a
		// page came back shorter than expected). Advance or finish.
		if skip >= len(page) {
			if !more {
				logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, no more songs lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
				tally.log(u.Username, lib.LibraryID, cfg.DryRun)
				return cur, true
			}
			cur.Offset = pageOffset + songPageSize
			continue
		}

		for i := skip; i < len(page); i++ {
			processSong(u, cfg, page[i], threshold, index, usePersistentIndex, lib.LibraryID, bucketCache, &tally)
			cur.Offset = pageOffset + i + 1
			cur.Retry = 0
			// Check the deadline only every deadlineCheckEvery songs so the
			// hot loop does not hammer the WASI clock import. Reducing the
			// rate also reduces the surface area for the host-side clock
			// panic we have seen in production (see callBudget docs).
			if (i-skip+1)%deadlineCheckEvery == 0 && time.Now().After(deadline) {
				logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, deadline reached lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
				tally.log(u.Username, lib.LibraryID, cfg.DryRun)
				return cur, false
			}
		}

		if !more {
			logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk done lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
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
