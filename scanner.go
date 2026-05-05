package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// ─── Configuration ────────────────────────────────────────────────────────────

type userConfig struct {
	Username         string
	TriggerUserScan  bool
	SkipAlreadyRated bool
	RatingTagOrder   []string
}

type libraryConfig struct {
	LibraryID   string
	LibraryName string
	Users       []userConfig
}

// pluginConfig holds values read from the Navidrome plugin settings UI.
type pluginConfig struct {
	// SyncSchedule is the cron expression for automatic recurring scans.
	SyncSchedule string
	// UserScanCooldownHours is the minimum gap (in hours) between two
	// user-triggered scans for the same user.
	UserScanCooldownHours int
	// MaxSongsPerRun caps the number of songs processed per scheduler run
	// per user. 0 = unlimited.
	MaxSongsPerRun int
	// Libraries holds per-library, per-user rating sync settings.
	Libraries []libraryConfig
}

// jsonUserConfig is used only for JSON unmarshaling of the libraries config.
type jsonUserConfig struct {
	Username         string   `json:"username"`
	TriggerUserScan  bool     `json:"trigger_user_scan"`
	SkipAlreadyRated *bool    `json:"skip_already_rated"` // pointer to detect absence (default: true)
	RatingTagOrder   []string `json:"ratingTagOrder"`
}

type jsonLibraryConfig struct {
	LibraryID   string           `json:"libraryId"`
	LibraryName string           `json:"libraryName"`
	Users       []jsonUserConfig `json:"users"`
}

var defaultTagOrder = []string{"WMP", "iTunes", "MediaMonkey"}

func loadConfig() pluginConfig {
	cfg := pluginConfig{
		SyncSchedule:          "0 */6 * * *",
		UserScanCooldownHours: 24,
		MaxSongsPerRun:        500,
	}

	if v, ok := pdk.GetConfig("sync_schedule"); ok {
		if s := strings.TrimSpace(v); s != "" {
			cfg.SyncSchedule = s
		}
	}
	if v, ok := pdk.GetConfig("user_scan_cooldown_hours"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			cfg.UserScanCooldownHours = n
		}
	}
	if v, ok := pdk.GetConfig("max_songs_per_run"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			cfg.MaxSongsPerRun = n
		} else {
			pdk.Log(pdk.LogWarn, fmt.Sprintf(
				"nd-rating-sync: invalid max_songs_per_run=%q – using default %d", v, cfg.MaxSongsPerRun))
		}
	}

	if v, ok := pdk.GetConfig("libraries"); ok && v != "" {
		var rawLibs []jsonLibraryConfig
		if err := json.Unmarshal([]byte(v), &rawLibs); err != nil {
			pdk.Log(pdk.LogWarn, "nd-rating-sync: failed to parse libraries config: "+err.Error())
		} else {
			for _, rl := range rawLibs {
				lc := libraryConfig{
					LibraryID:   rl.LibraryID,
					LibraryName: rl.LibraryName,
				}
				for _, ru := range rl.Users {
					uc := userConfig{
						Username:         ru.Username,
						TriggerUserScan:  ru.TriggerUserScan,
						SkipAlreadyRated: true, // default
						RatingTagOrder:   ru.RatingTagOrder,
					}
					if ru.SkipAlreadyRated != nil {
						uc.SkipAlreadyRated = *ru.SkipAlreadyRated
					}
					if len(uc.RatingTagOrder) == 0 {
						uc.RatingTagOrder = defaultTagOrder
					}
					lc.Users = append(lc.Users, uc)
				}
				cfg.Libraries = append(cfg.Libraries, lc)
			}
		}
	}

	pdk.Log(pdk.LogDebug, fmt.Sprintf(
		"nd-rating-sync: config – libraries=%d sync_schedule=%q max_songs_per_run=%d",
		len(cfg.Libraries), cfg.SyncSchedule, cfg.MaxSongsPerRun))
	return cfg
}

// ─── User-triggered scan ──────────────────────────────────────────────────────

var (
	lastUserScanMu    sync.Mutex
	lastUserScanTimes = map[string]time.Time{} // key: username
)

// checkAndRunUserTriggeredScan is called every 15 minutes. For each user who
// has trigger_user_scan=true and whose cooldown has elapsed, it runs a full
// sync scoped to that user and library.
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
					pdk.Log(pdk.LogInfo, fmt.Sprintf(
						"nd-rating-sync: user scan requested for %q but cooldown active (%.0f min remaining)",
						u.Username, remaining.Minutes()))
					continue
				}
			}

			pdk.Log(pdk.LogInfo, fmt.Sprintf(
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

// ─── Subsonic response types ──────────────────────────────────────────────────

type subsonicWrapper struct {
	Response subsonicResponse `json:"subsonic-response"`
}

type subsonicResponse struct {
	Status        string         `json:"status"`
	Error         *subsonicError `json:"error,omitempty"`
	SearchResult3 *searchResult3 `json:"searchResult3,omitempty"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type searchResult3 struct {
	Song []subsonicSong `json:"song"`
}

type subsonicSong struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Path       string `json:"path"`
	Suffix     string `json:"suffix"`
	UserRating int    `json:"userRating"` // 0 = unrated, 1–5 = stars
}

// ─── Sync ─────────────────────────────────────────────────────────────────────

// runSync is the top-level entry called from the scheduler callback. It iterates
// over every configured library/user combination and syncs ratings for each.
func runSync() error {
	cfg := loadConfig()
	if len(cfg.Libraries) == 0 {
		return errors.New("no libraries configured – add at least one library with users in the plugin settings")
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: starting sync – libraries=%d max_songs_per_run=%d",
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
		return errors.New("username is empty – check plugin configuration")
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: syncing user=%q library=%s skip_already_rated=%v tag_order=%v",
		u.Username, lib.LibraryID, u.SkipAlreadyRated, u.RatingTagOrder))

	songs, err := fetchAllSongs(u.Username, lib.LibraryID)
	if err != nil {
		return fmt.Errorf("fetching songs: %w", err)
	}

	rated, skippedRated, skippedNoTag, errored := 0, 0, 0, 0
	for i, s := range songs {
		if maxSongs > 0 && i >= maxSongs {
			pdk.Log(pdk.LogInfo, fmt.Sprintf(
				"nd-rating-sync: reached max_songs_per_run=%d for user=%q, stopping early",
				maxSongs, u.Username))
			break
		}

		if u.SkipAlreadyRated && s.UserRating > 0 {
			pdk.Log(pdk.LogDebug, fmt.Sprintf(
				"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
			skippedRated++
			continue
		}

		stars, ok := extractStarsFromFile(s.Path, s.Suffix, u.RatingTagOrder)
		if !ok {
			skippedNoTag++
			continue
		}

		if err := setRating(u.Username, s.ID, stars); err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf(
				"nd-rating-sync: setRating failed for %q (id=%s): %v", s.Title, s.ID, err))
			errored++
			continue
		}

		pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: rated %q → %d stars", s.Title, stars))
		rated++
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: done user=%q – rated=%d skipped_already_rated=%d skipped_no_tag=%d errors=%d",
		u.Username, rated, skippedRated, skippedNoTag, errored))
	return nil
}

// ─── Subsonic helpers ─────────────────────────────────────────────────────────

// fetchAllSongs pages through search3 and returns every song accessible by
// username in the given library (musicFolderId). Pass an empty libraryID to
// search across all libraries.
func fetchAllSongs(username, libraryID string) ([]subsonicSong, error) {
	const pageSize = 500
	var all []subsonicSong
	offset := 0

	for {
		uri := fmt.Sprintf(
			"search3?query=%%22%%22&songCount=%d&songOffset=%d&albumCount=0&artistCount=0&u=%s",
			pageSize, offset, username)
		if libraryID != "" {
			uri += "&musicFolderId=" + libraryID
		}

		pdk.Log(pdk.LogDebug, fmt.Sprintf(
			"nd-rating-sync: fetching songs – user=%q library=%s offset=%d page_size=%d",
			username, libraryID, offset, pageSize))

		raw, err := host.SubsonicAPICall(uri)
		if err != nil {
			return nil, fmt.Errorf("SubsonicAPICall (offset=%d): %w", offset, err)
		}

		var wrapper subsonicWrapper
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return nil, fmt.Errorf("unmarshal search3 response: %w", err)
		}
		if wrapper.Response.Status != "ok" {
			if wrapper.Response.Error != nil {
				return nil, fmt.Errorf("Subsonic API error %d: %s",
					wrapper.Response.Error.Code, wrapper.Response.Error.Message)
			}
			return nil, errors.New("Subsonic API returned non-ok status")
		}

		if wrapper.Response.SearchResult3 == nil {
			break
		}
		page := wrapper.Response.SearchResult3.Song
		all = append(all, page...)
		pdk.Log(pdk.LogDebug, fmt.Sprintf(
			"nd-rating-sync: page offset=%d returned %d songs (total so far: %d)",
			offset, len(page), len(all)))

		if len(page) < pageSize {
			break
		}
		offset += pageSize
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: found %d songs for user=%q library=%s", len(all), username, libraryID))
	return all, nil
}

// setRating calls the Subsonic setRating endpoint.
func setRating(username, songID string, stars int) error {
	uri := fmt.Sprintf("setRating?id=%s&rating=%d&u=%s", songID, stars, username)
	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		return err
	}

	var wrapper subsonicWrapper
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return fmt.Errorf("unmarshal setRating response: %w", err)
	}
	if wrapper.Response.Status != "ok" {
		if wrapper.Response.Error != nil {
			return fmt.Errorf("API error %d: %s",
				wrapper.Response.Error.Code, wrapper.Response.Error.Message)
		}
		return errors.New("setRating returned non-ok status")
	}
	return nil
}

// ─── File reading ─────────────────────────────────────────────────────────────

// extractStarsFromFile reads the audio file at path and returns a 1–5 star
// rating using the tag formats in tagOrder for priority, or (0, false) if no
// recognised rating tag is found.
func extractStarsFromFile(path, suffix string, tagOrder []string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("nd-rating-sync: cannot read %q: %v", path, err))
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
			pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		} else {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
		}
		return stars, ok
	default:
		pdk.Log(pdk.LogWarn, fmt.Sprintf(
			"nd-rating-sync: skipping %q – only MP3 files are supported (got .%s)", path, ext))
		return 0, false
	}
}
