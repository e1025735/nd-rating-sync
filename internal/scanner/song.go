package scanner

import (
	"fmt"
	"time"

	subsonicadapter "github.com/e1025735/nd-rating-sync/internal/adapter/subsonic"
	"github.com/e1025735/nd-rating-sync/internal/logging"
)

type subsonicSong = subsonicadapter.SubsonicSong

// processSong syncs one song. file is where locateSongFiles found the song's
// file; it is not consulted for a song skipped as already rated. logRead
// logs the file as a warning before it is read: after a crash, the last such
// line before the next stop names the file that crashes the plugin (the
// plugin cannot log once it has died).
func processSong(u UserConfig, cfg PluginConfig, s subsonicSong, threshold time.Time, file songFile, logRead bool, tally *syncTally) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: processSong start song=%q", s.ID))
	if u.SkipAlreadyRated && s.UserRating > 0 {
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, already rated song=%q", s.ID))
		logging.Debug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
		tally.skippedRated++
		return
	}

	if !file.found {
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, file not located song=%q", s.ID))
		logging.Warn(fmt.Sprintf(
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
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, no change song=%q", s.ID))
		logging.Debug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – unchanged since last scan (mtime=%s)",
			s.Title, entry.MTime.Format(time.RFC3339)))
		tally.skippedUnchanged++
		return
	}

	if logRead {
		logging.Warn(fmt.Sprintf("nd-rating-sync: reading %q (song %q, id=%q) after an unexpected stop", entry.Path, s.Title, s.ID))
	}
	stars, result := extractStarsFromFile(entry.Path, s.Suffix, u.RatingTagOrder)
	switch result {
	case FileUnreadable:
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, file unreadable song=%q", s.ID))
		tally.skippedUnreadable++
		return
	case TagNotWholeStar:
		// The file is rated, just not in whole stars: Navidrome cannot show
		// the value, and it must not be taken as 0 stars either.
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, not a whole-star rating song=%q", s.ID))
		logging.Debug(fmt.Sprintf("nd-rating-sync: skipping %q – the file's rating is a half star or out of range", s.Title))
		tally.skippedNotWholeStar++
		return
	case TagAbsent:
		// A file read to a clean end without a rating is rated 0 stars.
		if !u.ClearRatingIfUntagged {
			logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, no tag song=%q", s.ID))
			tally.skippedNoTag++
			return
		}
		// With clearing on, 0 stars is a rating like any other: when
		// Navidrome has none either, both already agree.
		if s.UserRating == 0 {
			logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, same rating (0) song=%q", s.ID))
			tally.skippedSameRating++
			return
		}
		if cfg.DryRun {
			logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, dry run would clear song=%q", s.ID))
			logging.Info(fmt.Sprintf("nd-rating-sync: [DRY RUN] would clear rating for %q (no tag found)", s.Title))
			tally.wouldClear++
			return
		}
		if err := subsonicadapter.SetRating(u.Username, s.ID, 0); err != nil {
			logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q", s.ID))
			logging.Warn(fmt.Sprintf(
				"nd-rating-sync: setRating(0) failed for %q (id=%q): %q", s.Title, s.ID, err.Error()))
			tally.errored++
			return
		}
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong done, cleared song=%q", s.ID))
		logging.Debug(fmt.Sprintf("nd-rating-sync: cleared rating for %q (no tag found)", s.Title))
		tally.cleared++
		return
	}

	// The rating from the song listing is at most one page old; writing the
	// same value again would only cost a host call and a database write.
	if stars == s.UserRating {
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, same rating song=%q", s.ID))
		logging.Debug(fmt.Sprintf("nd-rating-sync: skipping %q – Navidrome already has %d stars", s.Title, stars))
		tally.skippedSameRating++
		return
	}
	if cfg.DryRun {
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, dry run would rate song=%q", s.ID))
		logging.Info(fmt.Sprintf("nd-rating-sync: [DRY RUN] would rate %q → %d stars", s.Title, stars))
		tally.wouldRate++
		return
	}
	if err := subsonicadapter.SetRating(u.Username, s.ID, stars); err != nil {
		logging.Trace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q", s.ID))
		logging.Warn(fmt.Sprintf(
			"nd-rating-sync: setRating failed for %q (id=%q): %q", s.Title, s.ID, err.Error()))
		tally.errored++
		return
	}
	logging.Trace(fmt.Sprintf("nd-rating-sync: setRating done song=%q", s.ID))
	logging.Debug(fmt.Sprintf("nd-rating-sync: rated %q → %d stars", s.Title, stars))
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
