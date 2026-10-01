package main

import (
	"fmt"
	"time"

	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
)

type subsonicSong = subsonicadapter.SubsonicSong

// processSong applies the rating pipeline to one song: skip-if-already-rated,
// locate the real file under the library mount, skip-if-unchanged (incremental),
// read+parse the file, then write or clear the rating. Outcomes are accumulated
// into tally. A file that cannot be located, read, or parsed is treated as
// fileUnreadable – never as "untagged" – so clear_rating_if_untagged can never
// wipe a rating on a transient I/O error or an unmatched file.
func processSong(u userConfig, cfg pluginConfig, s subsonicSong, threshold time.Time, index map[string][]fileEntry, usePersistentIndex bool, libraryID string, bucketCache map[string][]FileRecord, tally *syncTally) {
	logTrace(fmt.Sprintf("nd-rating-sync: processSong start song=%q, threshold=%q", s.ID, threshold))
	if u.SkipAlreadyRated && s.UserRating > 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, already rated song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
		tally.skippedRated++
		return
	}

	// Locate the file under the library mount. Navidrome's Subsonic `path`
	// field is a synthesized fake by default (see helpers.fakePath in the
	// server), so we cannot open it directly; instead we match on the
	// reported byte size + suffix that the host's scanner stored. A missing or
	// ambiguous match is treated as unreadable – never as "no tag found" – so
	// clear_rating_if_untagged can never wipe a rating for a file we could not
	// positively identify on disk.
	var entry fileEntry
	var found bool
	if usePersistentIndex {
		entry, found = matchFileFromBucketCache(libraryID, s, bucketCache)
	} else {
		entry, found = matchFile(index, s)
	}
	if !found {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, ambiguous file song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: no unique file for %q (size=%d suffix=%q) – skipping",
			s.Title, s.Size, s.Suffix))
		tally.skippedUnreadable++
		return
	}

	if !threshold.IsZero() && entry.mtime.Before(threshold) {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no change song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – unchanged since last scan (mtime=%s)",
			s.Title, entry.mtime.Format(time.RFC3339)))
		tally.skippedUnchanged++
		return
	}

	stars, result := extractStarsFromFile(entry.path, s.Suffix, u.RatingTagOrder)
	switch result {
	case fileUnreadable:
		// I/O error, unsupported extension, or parse panic. Never clear here —
		// clearing on a transient read failure would corrupt the user's
		// existing Navidrome rating. The warning was already logged inside
		// extractStarsFromFile; just count and move on.
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, file unreadable song=%q, threshold=%q", s.ID, threshold))
		tally.skippedUnreadable++
		return
	case tagAbsent:
		if !u.ClearRatingIfUntagged {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no tag song=%q, threshold=%q", s.ID, threshold))
			tally.skippedNoTag++
			return
		}
		if cfg.DryRun {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, not allowed to clear song=%q, threshold=%q", s.ID, threshold))
			logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would clear rating for %q (no tag found)", s.Title))
			tally.wouldClear++
			return
		}
		if err := subsonicadapter.SetRating(u.Username, s.ID, 0); err != nil {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q, threshold=%q", s.ID, threshold))
			logWarn(fmt.Sprintf(
				"nd-rating-sync: setRating(0) failed for %q (id=%q): %v", s.Title, s.ID, err))
			tally.errored++
			return
		}
		logTrace(fmt.Sprintf("nd-rating-sync: processSong done, file unreadable song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf("nd-rating-sync: cleared rating for %q (no tag found)", s.Title))
		tally.cleared++
		return
	}

	// result == tagFound
	if cfg.DryRun {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, done song=%q, threshold=%q", s.ID, threshold))
		logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would rate %q → %d stars", s.Title, stars))
		tally.wouldRate++
		return
	}
	if err := subsonicadapter.SetRating(u.Username, s.ID, stars); err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q, threshold=%q", s.ID, threshold))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: setRating failed for %q (id=%q): %v", s.Title, s.ID, err))
		tally.errored++
		return
	}
	logTrace(fmt.Sprintf("nd-rating-sync: setRating done song=%q, threshold=%q", s.ID, threshold))
	logDebug(fmt.Sprintf("nd-rating-sync: rated %q → %d stars", s.Title, stars))
	tally.rated++
}
