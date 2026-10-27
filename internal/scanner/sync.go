package scanner

import (
	"fmt"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
	"github.com/e1025735/nd-rating-sync/internal/logging"
)

const songPageSize = subsonicadapter.SongPageSize

// pairRun is the (library, user) pair processPairChunk works on, with what
// runSyncChunk resolved for it.
type pairRun struct {
	lib       LibraryConfig
	user      UserConfig
	cfg       PluginConfig
	mount     libraryMount
	threshold time.Time // incremental threshold; zero reads every file
}

// runSyncChunk processes as many songs as fit before deadline, starting from
// cur, and returns the position to resume at plus whether the whole sweep is
// finished. It walks (library, user) pairs in order: for each fresh pair it
// stamps PairStart (the eventual incremental threshold) and, once the pair is
// fully processed, persists that threshold. When the deadline is reached
// mid-sweep it returns allDone=false with a cursor the caller hands to a
// continuation callback.
//
// Forward progress is guaranteed: every invocation either processes at least
// one song or counts a failed attempt in cur.Retry, and a pair is skipped
// after maxPairAttempts failed attempts – so a chain of continuations always
// terminates.
func runSyncChunk(cfg PluginConfig, cur SyncCursor, deadline time.Time) (SyncCursor, bool) {
	lookups := newHostLookups()

	logging.Trace(fmt.Sprintf("nd-rating-sync: runSyncChunk start lib=%d user=%d offset=%d deadline=%s",
		cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
	for {
		// Skip exhausted users/libraries. Also tolerates indices that point
		// past the end after a config change between continuations.
		for cur.Lib < len(cfg.Libraries) && cur.User >= len(cfg.Libraries[cur.Lib].Users) {
			cur.nextLibrary()
		}
		if cur.Lib >= len(cfg.Libraries) {
			logging.Trace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, sweep complete lib=%d user=%d offset=%d deadline=%s",
				cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
			return cur, true // whole sweep complete
		}
		// A valid pair is selected. If the budget is gone, resume here.
		if time.Now().After(deadline) {
			logging.Trace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline reached lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
			return cur, false
		}

		lib := cfg.Libraries[cur.Lib]
		u := lib.Users[cur.User]
		logging.Debug(fmt.Sprintf("nd-rating-sync: will look for the following ratings: %q", u.RatingTagOrder))
		if u.Username == "" {
			logging.Warn(fmt.Sprintf(
				"nd-rating-sync: skipping library=%q user#%d – empty username in config", lib.LibraryID, cur.User))
			cur.nextPair()
			continue
		}

		// Before a pair starts, check the user and library against what the
		// plugin may access: a typo or a missing grant is reported plainly
		// and skipped at once, instead of failing the song listing three
		// times 30 s apart on every sweep.
		if cur.Offset == 0 && cur.PairStart == "" {
			if reason := lookups.problem(lib.LibraryID, u.Username); reason != "" {
				logging.Warn(fmt.Sprintf("nd-rating-sync: skipping user=%q library=%q – %s", u.Username, lib.LibraryID, reason))
				cur.nextPair()
				continue
			}
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
			if scanned, ok := lookups.lastScan(lib.LibraryID); ok && scanned.Before(threshold) {
				logging.Info(fmt.Sprintf(
					"nd-rating-sync: skipping user=%q library=%q – library unchanged since last sync (last_scan=%s threshold=%s)",
					u.Username, lib.LibraryID, scanned.UTC().Format(time.RFC3339), threshold.UTC().Format(time.RFC3339)))
				cur.nextPair()
				continue
			}
		}

		// The songs' files are read through the library's sandbox mount. A
		// library the plugin cannot read is skipped WITHOUT saving the
		// threshold – nothing was processed, so the next run retries from the
		// same baseline.
		mount, err := lookups.mount(lib.LibraryID)
		if err != nil {
			logging.Warn(fmt.Sprintf(
				"nd-rating-sync: skipping user=%q library=%q – cannot access its files: %q", u.Username, lib.LibraryID, err.Error()))
			cur.nextPair()
			continue
		}

		if cur.PairStart == "" {
			cur.PairStart = time.Now().UTC().Format(time.RFC3339Nano)
		}

		next, pairDone := processPairChunk(pairRun{lib: lib, user: u, cfg: cfg, mount: mount, threshold: threshold}, cur, deadline)
		cur = next
		if !pairDone {
			if cur.Retry >= maxPairAttempts {
				logging.Warn(fmt.Sprintf(
					"nd-rating-sync: skipping user=%q library=%q until the next sweep – listing or locating its songs failed %d times in a row",
					u.Username, lib.LibraryID, cur.Retry))
				cur.nextPair()
				continue
			}
			logging.Trace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit in processPairChunk lib=%d user=%d offset=%d retry=%d deadline=%s", cur.Lib, cur.User, cur.Offset, cur.Retry, deadline.Format(time.RFC3339Nano)))
			return cur, false // deadline hit (or a page failed) mid-pair
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

// processPairChunk processes songs for the pair p starting at cur.Offset until the deadline elapses or the pair's songs are exhausted.
// It returns the advanced cursor and whether the pair is complete. The deadline
// is checked after each processed song, so at least one song is handled per
// call (provided the deadline had not already passed on entry, which
// runSyncChunk guarantees) – this is what makes the continuation chain
// terminate.
//
// A page that cannot be fetched, or whose songs the matcher cannot look up,
// returns pairDone=false without advancing past it and counts the failure in
// cur.Retry, so the continuation retries the same offset after a delay and
// runSyncChunk gives up on the pair after maxPairAttempts. Every processed
// song resets cur.Retry.
//
// The position is stored in the sweep record at the start of every page, so
// a callback that crashes on a page is resumed at that page's start (see
// startOrResumeSweep). While cur.LogReadsUntil is set, every file read up to
// that offset is logged as a warning first; once the cursor passes it,
// logging is normal again.
func processPairChunk(p pairRun, cur SyncCursor, deadline time.Time) (SyncCursor, bool) {
	lib, u, cfg := p.lib, p.user, p.cfg
	logging.Trace(fmt.Sprintf("nd-rating-sync: processPairChunk start lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
	if cfg.DryRun {
		logging.Info(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] user=%q – no ratings will be written", u.Username))
	}
	if u.ClearRatingIfUntagged && u.SkipAlreadyRated {
		logging.Warn(fmt.Sprintf(
			"nd-rating-sync: user=%q has clear_rating_if_untagged=true but skip_already_rated=true – "+
				"songs already rated in Navidrome will be skipped before the file is read and their ratings will NOT be cleared",
			u.Username))
	}
	logging.Info(fmt.Sprintf(
		"nd-rating-sync: syncing user=%q library=%q from offset=%d skip_already_rated=%v clear_rating_if_untagged=%v dry_run=%v tag_order=%q incremental_threshold=%s",
		u.Username, lib.LibraryID, cur.Offset, u.SkipAlreadyRated, u.ClearRatingIfUntagged, cfg.DryRun, u.RatingTagOrder, formatThreshold(p.threshold)))

	var tally syncTally
	defer func() { tally.log(u.Username, lib.LibraryID, cfg.DryRun) }()
	for {
		pageOffset := (cur.Offset / songPageSize) * songPageSize
		skip := cur.Offset - pageOffset
		kvadapter.MarkSweepActive(cur.marshal())

		page, more, err := subsonicadapter.FetchSongPage(u.Username, lib.LibraryID, pageOffset, songPageSize)
		if err != nil {
			cur.Retry++
			logging.Warn(fmt.Sprintf(
				"nd-rating-sync: fetching songs for user=%q library=%q at offset=%d failed (attempt %d of %d): %q",
				u.Username, lib.LibraryID, pageOffset, cur.Retry, maxPairAttempts, err.Error()))
			return cur, false
		}

		// Resume position is at/past the end of this page (only happens when a
		// page came back shorter than expected). Advance or finish.
		if skip >= len(page) {
			if !more {
				logging.Trace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, no more songs lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
				return cur, true
			}
			cur.Offset = pageOffset + songPageSize
			continue
		}

		files, err := locateSongFiles(p.mount, u, page[skip:])
		if err != nil {
			cur.Retry++
			logging.Warn(fmt.Sprintf(
				"nd-rating-sync: locating the files of user=%q library=%q at offset=%d failed (attempt %d of %d): %q",
				u.Username, lib.LibraryID, cur.Offset, cur.Retry, maxPairAttempts, err.Error()))
			return cur, false
		}

		for i := skip; i < len(page); i++ {
			logRead := pageOffset+i < cur.LogReadsUntil
			processSong(u, cfg, page[i], p.threshold, files[page[i].ID], logRead, &tally)
			cur.Offset = pageOffset + i + 1
			cur.Retry = 0
			if cur.LogReadsUntil > 0 && cur.Offset >= cur.LogReadsUntil {
				logging.Info(fmt.Sprintf(
					"nd-rating-sync: user=%q library=%q – the songs up to offset %d were read without a stop; file reads are no longer logged",
					u.Username, lib.LibraryID, cur.LogReadsUntil))
				cur.LogReadsUntil = 0
			}
			// Checked after every song: one slow song must not carry the
			// callback far past the budget (see callBudget).
			if time.Now().After(deadline) {
				logging.Trace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, deadline reached lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
				return cur, false
			}
		}

		if !more {
			logging.Trace(fmt.Sprintf("nd-rating-sync: processPairChunk done lib=%d user=%d offset=%d deadline=%s", cur.Lib, cur.User, cur.Offset, deadline.Format(time.RFC3339Nano)))
			return cur, true
		}
		// Page was full; cur.Offset is already at the next page boundary.
	}
}

// syncTally accumulates per-chunk outcome counts for a single summary log line.
type syncTally struct {
	rated, cleared, wouldRate, wouldClear         int
	skippedRated, skippedNoTag, skippedUnchanged  int
	skippedUnreadable, skippedSameRating, errored int
}

func (t syncTally) log(username, libraryID string, dryRun bool) {
	if dryRun {
		logging.Info(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] chunk done user=%q library=%q – would_rate=%d would_clear=%d skipped_already_rated=%d skipped_same_rating=%d skipped_unchanged=%d skipped_no_tag=%d skipped_unreadable=%d",
			username, libraryID, t.wouldRate, t.wouldClear, t.skippedRated, t.skippedSameRating, t.skippedUnchanged, t.skippedNoTag, t.skippedUnreadable))
		return
	}
	logging.Info(fmt.Sprintf(
		"nd-rating-sync: chunk done user=%q library=%q – rated=%d cleared=%d skipped_already_rated=%d skipped_same_rating=%d skipped_unchanged=%d skipped_no_tag=%d skipped_unreadable=%d errors=%d",
		username, libraryID, t.rated, t.cleared, t.skippedRated, t.skippedSameRating, t.skippedUnchanged, t.skippedNoTag, t.skippedUnreadable, t.errored))
}

// formatThreshold renders a threshold timestamp for log output, with a
// distinct marker when no threshold has been recorded yet.
func formatThreshold(t time.Time) string {
	if t.IsZero() {
		return "(none – full scan)"
	}
	return t.UTC().Format(time.RFC3339)
}
