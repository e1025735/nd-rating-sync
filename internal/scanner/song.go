package scanner

import (
	"fmt"
	"time"

	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
)

type subsonicSong = subsonicadapter.SubsonicSong

func processSong(u UserConfig, cfg PluginConfig, s subsonicSong, threshold time.Time, index map[string][]FileEntry, usePersistentIndex bool, libraryID string, bucketCache map[string][]FileRecord, tally *syncTally) {
	logTrace(fmt.Sprintf("nd-rating-sync: processSong start song=%q, threshold=%q", s.ID, threshold))
	if u.SkipAlreadyRated && s.UserRating > 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, already rated song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
		tally.skippedRated++
		return
	}

	var entry FileEntry
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

	if !threshold.IsZero() && entry.MTime.Before(threshold) {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no change song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – unchanged since last scan (mtime=%s)",
			s.Title, entry.MTime.Format(time.RFC3339)))
		tally.skippedUnchanged++
		return
	}

	stars, result := ExtractStarsFromFile(entry.Path, s.Suffix, u.RatingTagOrder)
	switch result {
	case FileUnreadable:
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, file unreadable song=%q, threshold=%q", s.ID, threshold))
		tally.skippedUnreadable++
		return
	case TagAbsent:
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
