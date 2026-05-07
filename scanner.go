package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─── User-triggered scan ──────────────────────────────────────────────────────

var (
	lastUserScanMu    sync.Mutex
	lastUserScanTimes = map[string]time.Time{} // key: username
)

// checkAndRunUserTriggeredScan is called every 15 minutes.
func checkAndRunUserTriggeredScan() error {
	return checkAndRunUserTriggeredScanWith(loadConfig())
}

// checkAndRunUserTriggeredScanWith is the testable variant that takes the
// already-resolved config rather than reading it from the PDK.
func checkAndRunUserTriggeredScanWith(cfg pluginConfig) error {
	var errMsgs []string
	for _, lib := range cfg.Libraries {
		for _, u := range lib.Users {
			if !u.TriggerUserScan {
				continue
			}

			lastUserScanMu.Lock()
			last := lastUserScanTimes[u.Username]
			lastUserScanMu.Unlock()

			if cfg.UserScanCooldownHours > 0 && !last.IsZero() {
				cooldown := time.Duration(cfg.UserScanCooldownHours) * time.Hour
				remaining := cooldown - time.Since(last)
				if remaining > 0 {
					logInfo(fmt.Sprintf(
						"nd-rating-sync: user scan requested for %q but cooldown active (%.0f min remaining)",
						u.Username, remaining.Minutes()))
					continue
				}
			}

			logInfo(fmt.Sprintf(
				"nd-rating-sync: running user-triggered rating sync for %q (library=%s)",
				u.Username, lib.LibraryID))

			lastUserScanMu.Lock()
			lastUserScanTimes[u.Username] = time.Now()
			lastUserScanMu.Unlock()

			if err := runSyncForUser(lib, u, cfg); err != nil {
				errMsgs = append(errMsgs, fmt.Sprintf("user=%s lib=%s: %v", u.Username, lib.LibraryID, err))
			}
		}
	}

	if len(errMsgs) > 0 {
		return fmt.Errorf("user-triggered sync errors: %s", strings.Join(errMsgs, "; "))
	}
	return nil
}

// ─── Sync ─────────────────────────────────────────────────────────────────────

// runSync iterates over every configured library/user combination.
func runSync() error { return runSyncWith(loadConfig()) }

// runSyncWith is the testable variant that takes the resolved config.
func runSyncWith(cfg pluginConfig) error {
	if len(cfg.Libraries) == 0 {
		return errors.New("no libraries configured – add at least one library with users in the plugin settings")
	}

	logInfo(fmt.Sprintf(
		"nd-rating-sync: starting sync – libraries=%d max_songs_per_run=%d incremental=%v dry_run=%v",
		len(cfg.Libraries), cfg.MaxSongsPerRun, cfg.IncrementalSync, cfg.DryRun))

	var errMsgs []string
	for _, lib := range cfg.Libraries {
		for _, u := range lib.Users {
			if err := runSyncForUser(lib, u, cfg); err != nil {
				errMsgs = append(errMsgs, fmt.Sprintf("user=%s lib=%s: %v", u.Username, lib.LibraryID, err))
			}
		}
	}

	if len(errMsgs) > 0 {
		return fmt.Errorf("sync errors: %s", strings.Join(errMsgs, "; "))
	}
	return nil
}

// runSyncForUser fetches and processes songs for a single library/user pair.
//
// When cfg.IncrementalSync is true, the previous successful scan time is
// loaded from the KV store and any song whose file mtime predates it is
// skipped without reading the file or calling setRating. The scan-start
// time is captured before iterating and persisted at the end, so any file
// edits that occur during the scan are caught on the next run.
func runSyncForUser(lib libraryConfig, u userConfig, cfg pluginConfig) error {
	if u.Username == "" {
		return errors.New("username is empty – check plugin configuration")
	}

	var threshold time.Time
	if cfg.IncrementalSync {
		threshold = loadLastSynced(lib.LibraryID, u.Username)
	}
	scanStart := time.Now()

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
		"nd-rating-sync: syncing user=%q library=%s skip_already_rated=%v clear_rating_if_untagged=%v dry_run=%v tag_order=%v incremental_threshold=%s",
		u.Username, lib.LibraryID, u.SkipAlreadyRated, u.ClearRatingIfUntagged, cfg.DryRun, u.RatingTagOrder, formatThreshold(threshold)))

	songs, err := fetchAllSongs(u.Username, lib.LibraryID)
	if err != nil {
		return fmt.Errorf("fetching songs: %w", err)
	}

	rated, cleared, wouldRate, wouldClear, skippedRated, skippedNoTag, skippedUnchanged, errored := 0, 0, 0, 0, 0, 0, 0, 0
	for i, s := range songs {
		if cfg.MaxSongsPerRun > 0 && i >= cfg.MaxSongsPerRun {
			logInfo(fmt.Sprintf(
				"nd-rating-sync: reached max_songs_per_run=%d for user=%q, stopping early",
				cfg.MaxSongsPerRun, u.Username))
			break
		}

		if u.SkipAlreadyRated && s.UserRating > 0 {
			logDebug(fmt.Sprintf(
				"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
			skippedRated++
			continue
		}

		if !threshold.IsZero() {
			if info, err := os.Stat(s.Path); err == nil && info.ModTime().Before(threshold) {
				logDebug(fmt.Sprintf(
					"nd-rating-sync: skipping %q – unchanged since last scan (mtime=%s)",
					s.Title, info.ModTime().Format(time.RFC3339)))
				skippedUnchanged++
				continue
			}
		}

		stars, ok := extractStarsFromFile(s.Path, s.Suffix, u.RatingTagOrder)
		if !ok {
			if u.ClearRatingIfUntagged {
				if cfg.DryRun {
					logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would clear rating for %q (no tag found)", s.Title))
					wouldClear++
				} else {
					if err := setRating(u.Username, s.ID, 0); err != nil {
						logWarn(fmt.Sprintf(
							"nd-rating-sync: setRating(0) failed for %q (id=%s): %v", s.Title, s.ID, err))
						errored++
					} else {
						logDebug(fmt.Sprintf("nd-rating-sync: cleared rating for %q (no tag found)", s.Title))
						cleared++
					}
				}
			} else {
				skippedNoTag++
			}
			continue
		}

		if cfg.DryRun {
			logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would rate %q → %d stars", s.Title, stars))
			wouldRate++
			continue
		}

		if err := setRating(u.Username, s.ID, stars); err != nil {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: setRating failed for %q (id=%s): %v", s.Title, s.ID, err))
			errored++
			continue
		}

		logDebug(fmt.Sprintf("nd-rating-sync: rated %q → %d stars", s.Title, stars))
		rated++
	}

	if cfg.DryRun {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] done user=%q – would_rate=%d would_clear=%d skipped_already_rated=%d skipped_unchanged=%d skipped_no_tag=%d",
			u.Username, wouldRate, wouldClear, skippedRated, skippedUnchanged, skippedNoTag))
	} else {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: done user=%q – rated=%d cleared=%d skipped_already_rated=%d skipped_unchanged=%d skipped_no_tag=%d errors=%d",
			u.Username, rated, cleared, skippedRated, skippedUnchanged, skippedNoTag, errored))
	}

	if cfg.IncrementalSync && !cfg.DryRun {
		saveLastSynced(lib.LibraryID, u.Username, scanStart)
	}
	return nil
}

// formatThreshold renders a threshold timestamp for log output, with a
// distinct marker when no threshold has been recorded yet.
func formatThreshold(t time.Time) string {
	if t.IsZero() {
		return "(none – full scan)"
	}
	return t.UTC().Format(time.RFC3339)
}

// ─── File reading ─────────────────────────────────────────────────────────────

// extractStarsFromFile reads the audio file at path and returns a 1–5 star
// rating using the tag formats in tagOrder for priority.
func extractStarsFromFile(path, suffix string, tagOrder []string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: cannot read %q: %v", path, err))
		return 0, false
	}

	ext := strings.ToLower(suffix)
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}

	switch ext {
	case "mp3":
		stars, ok := parseID3v2Rating(data, tagOrder)
		if ok {
			logDebug(fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		} else {
			logDebug(fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
		}
		return stars, ok
	case "flac":
		stars, ok := parseFLACRating(data, tagOrder)
		if ok {
			logDebug(fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		} else {
			logDebug(fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
		}
		return stars, ok
	case "ogg", "oga", "opus":
		stars, ok := parseOggVorbisRating(data, tagOrder)
		if ok {
			logDebug(fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		} else {
			logDebug(fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
		}
		return stars, ok
	default:
		logWarn(fmt.Sprintf(
			"nd-rating-sync: skipping %q – only MP3, FLAC, OGG and Opus files are supported (got .%s)", path, ext))
		return 0, false
	}
}