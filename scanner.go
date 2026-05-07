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

// â”€â”€â”€ User-triggered scan â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

var (
	lastUserScanMu    sync.Mutex
	lastUserScanTimes = map[string]time.Time{} // key: username
)

/*
checkAndRunUserTriggeredScan is called every 15 minutes. For each user who
has trigger_user_scan=true and whose cooldown has elapsed, it runs a full
sync scoped to that user and library.
*/
func checkAndRunUserTriggeredScan() error {
	cfg := loadConfig()

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

			if err := runSyncForUser(lib, u, cfg.MaxSongsPerRun); err != nil {
				errMsgs = append(errMsgs, fmt.Sprintf("user=%s lib=%s: %v", u.Username, lib.LibraryID, err))
			}
		}
	}

	if len(errMsgs) > 0 {
		return fmt.Errorf("user-triggered sync errors: %s", strings.Join(errMsgs, "; "))
	}
	return nil
}

// â”€â”€â”€ Sync â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

// runSync iterates over every configured library/user combination and syncs ratings.
func runSync() error {
	cfg := loadConfig()
	if len(cfg.Libraries) == 0 {
		return errors.New("no libraries configured â€“ add at least one library with users in the plugin settings")
	}

	logInfo(fmt.Sprintf(
		"nd-rating-sync: starting sync â€“ libraries=%d max_songs_per_run=%d",
		len(cfg.Libraries), cfg.MaxSongsPerRun))

	var errMsgs []string
	for _, lib := range cfg.Libraries {
		for _, u := range lib.Users {
			if err := runSyncForUser(lib, u, cfg.MaxSongsPerRun); err != nil {
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
func runSyncForUser(lib libraryConfig, u userConfig, maxSongs int) error {
	if u.Username == "" {
		return errors.New("username is empty â€“ check plugin configuration")
	}

	logInfo(fmt.Sprintf(
		"nd-rating-sync: syncing user=%q library=%s skip_already_rated=%v tag_order=%v",
		u.Username, lib.LibraryID, u.SkipAlreadyRated, u.RatingTagOrder))

	songs, err := fetchAllSongs(u.Username, lib.LibraryID)
	if err != nil {
		return fmt.Errorf("fetching songs: %w", err)
	}

	rated, skippedRated, skippedNoTag, errored := 0, 0, 0, 0
	for i, s := range songs {
		if maxSongs > 0 && i >= maxSongs {
			logInfo(fmt.Sprintf(
				"nd-rating-sync: reached max_songs_per_run=%d for user=%q, stopping early",
				maxSongs, u.Username))
			break
		}

		if u.SkipAlreadyRated && s.UserRating > 0 {
			logDebug(fmt.Sprintf(
				"nd-rating-sync: skipping %q â€“ already rated (%d stars in Navidrome)", s.Title, s.UserRating))
			skippedRated++
			continue
		}

		stars, ok := extractStarsFromFile(s.Path, s.Suffix, u.RatingTagOrder)
		if !ok {
			skippedNoTag++
			continue
		}

		if err := setRating(u.Username, s.ID, stars); err != nil {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: setRating failed for %q (id=%s): %v", s.Title, s.ID, err))
			errored++
			continue
		}

		logDebug(fmt.Sprintf("nd-rating-sync: rated %q â†’ %d stars", s.Title, stars))
		rated++
	}

	logInfo(fmt.Sprintf(
		"nd-rating-sync: done user=%q â€“ rated=%d skipped_already_rated=%d skipped_no_tag=%d errors=%d",
		u.Username, rated, skippedRated, skippedNoTag, errored))
	return nil
}

// â”€â”€â”€ File reading â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€

/*
extractStarsFromFile reads the audio file at path and returns a 1â€“5 star
rating using the tag formats in tagOrder for priority, or (0, false) if no
recognised rating tag is found.
*/
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
			logDebug(fmt.Sprintf("nd-rating-sync: %q â€“ found rating tag â†’ %d stars", path, stars))
		} else {
			logDebug(fmt.Sprintf("nd-rating-sync: %q â€“ no rating tag found", path))
		}
		return stars, ok
	default:
		logWarn(fmt.Sprintf(
			"nd-rating-sync: skipping %q â€“ only MP3 files are supported (got .%s)", path, ext))
		return 0, false
	}
}
