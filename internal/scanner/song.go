package scanner

import (
	"fmt"
	"time"

	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
)

type subsonicSong = subsonicadapter.SubsonicSong

// processSong syncs one song. file is where locateSongFiles found the song's
// file; it is not consulted for a song skipped as already rated.
func processSong(u UserConfig, cfg PluginConfig, s subsonicSong, threshold time.Time, file songFile, tally *syncTally) {
	logTrace(fmt.Sprintf("nd-rating-sync: processSong start song=%q", s.ID))
	if u.SkipAlreadyRated && s.UserRating > 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, already rated song=%q", s.ID))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
		tally.skippedRated++
		return
	}

	if !file.found {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, file not located song=%q", s.ID))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot locate the file of %q (id=%q): %s – skipping",
			s.Title, s.ID, file.reason))
		tally.skippedUnreadable++
		return
	}
	entry := file.entry

	// A renamed/moved file keeps its old mtime but is imported by Navidrome as
	// a new, unrated song – so a song added since the last sweep is processed
	// even when the file itself looks unchanged.
	if !threshold.IsZero() && entry.MTime.Before(threshold) && !songAddedSince(s, threshold) {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no change song=%q", s.ID))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – unchanged since last scan (mtime=%s)",
			s.Title, entry.MTime.Format(time.RFC3339)))
		tally.skippedUnchanged++
		return
	}

	stars, result := extractStarsFromFile(entry.Path, s.Suffix, u.RatingTagOrder)
	switch result {
	case FileUnreadable:
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, file unreadable song=%q", s.ID))
		tally.skippedUnreadable++
		return
	case TagAbsent:
		if !u.ClearRatingIfUntagged {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no tag song=%q", s.ID))
			tally.skippedNoTag++
			return
		}
		if cfg.DryRun {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, dry run would clear song=%q", s.ID))
			logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would clear rating for %q (no tag found)", s.Title))
			tally.wouldClear++
			return
		}
		if err := subsonicadapter.SetRating(u.Username, s.ID, 0); err != nil {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q", s.ID))
			logWarn(fmt.Sprintf(
				"nd-rating-sync: setRating(0) failed for %q (id=%q): %q", s.Title, s.ID, err.Error()))
			tally.errored++
			return
		}
		logTrace(fmt.Sprintf("nd-rating-sync: processSong done, cleared song=%q", s.ID))
		logDebug(fmt.Sprintf("nd-rating-sync: cleared rating for %q (no tag found)", s.Title))
		tally.cleared++
		return
	}

	if cfg.DryRun {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, dry run would rate song=%q", s.ID))
		logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would rate %q → %d stars", s.Title, stars))
		tally.wouldRate++
		return
	}
	if err := subsonicadapter.SetRating(u.Username, s.ID, stars); err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q", s.ID))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: setRating failed for %q (id=%q): %q", s.Title, s.ID, err.Error()))
		tally.errored++
		return
	}
	logTrace(fmt.Sprintf("nd-rating-sync: setRating done song=%q", s.ID))
	logDebug(fmt.Sprintf("nd-rating-sync: rated %q → %d stars", s.Title, stars))
	tally.rated++
}

// songAddedSince reports whether Navidrome added s at or after threshold. A
// missing or unparseable `created` value reports false, which falls back to
// the plain mtime check.
func songAddedSince(s subsonicSong, threshold time.Time) bool {
	if s.Created == "" {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, s.Created)
	if err != nil {
		return false
	}
	return !created.Before(threshold)
}
