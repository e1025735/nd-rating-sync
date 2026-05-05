package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// ─── Configuration ────────────────────────────────────────────────────────────

// pluginConfig holds values read from the Navidrome plugin settings UI.
type pluginConfig struct {
	// Username of the Navidrome account used for API calls.
	// Required – the Subsonic API always needs a `u` parameter.
	Username string
	// SyncSchedule is the cron expression for automatic recurring scans.
	// Set by the admin; defaults to every 6 hours.
	SyncSchedule string
	// UserScanCooldownHours is the minimum gap (in hours) between two
	// user-triggered scans.  Set by the admin; defaults to 24 (once per day).
	UserScanCooldownHours int
	// TriggerUserScan is toggled to "true" by a user to request an immediate scan.
	TriggerUserScan bool
	// SkipAlreadyRated controls whether songs that already have a user rating
	// in Navidrome are left untouched.  Defaults to true.
	SkipAlreadyRated bool
	// MaxSongsPerRun caps the number of songs processed per scheduler run to
	// avoid very long-running tasks on large libraries.  0 = unlimited.
	MaxSongsPerRun int
}

func loadConfig() pluginConfig {
	cfg := pluginConfig{
		SyncSchedule:          "0 */6 * * *",
		UserScanCooldownHours: 24,
		SkipAlreadyRated:      true,
		MaxSongsPerRun:        500,
	}
	if v, ok := pdk.GetConfig("username"); ok {
		cfg.Username = strings.TrimSpace(v)
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
	if v, ok := pdk.GetConfig("trigger_user_scan"); ok {
		cfg.TriggerUserScan = strings.ToLower(strings.TrimSpace(v)) == "true"
	}
	if v, ok := pdk.GetConfig("skip_already_rated"); ok {
		cfg.SkipAlreadyRated = strings.ToLower(strings.TrimSpace(v)) != "false"
	}
	if v, ok := pdk.GetConfig("max_songs_per_run"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			cfg.MaxSongsPerRun = n
		}
	}
	return cfg
}

// ─── User-triggered scan ──────────────────────────────────────────────────────

var (
	lastUserScanMu   sync.Mutex
	lastUserScanTime time.Time // zero = never triggered
)

// checkAndRunUserTriggeredScan is called every 15 minutes. It runs a full sync
// when all of these are true:
//  1. trigger_user_scan is set to "true" in the plugin config
//  2. the cooldown period configured by the admin has elapsed since the last
//     user-triggered scan (or no such scan has happened since plugin load)
func checkAndRunUserTriggeredScan() error {
	cfg := loadConfig()
	if !cfg.TriggerUserScan {
		return nil
	}

	lastUserScanMu.Lock()
	last := lastUserScanTime
	lastUserScanMu.Unlock()

	if cfg.UserScanCooldownHours > 0 && !last.IsZero() {
		cooldown := time.Duration(cfg.UserScanCooldownHours) * time.Hour
		remaining := cooldown - time.Since(last)
		if remaining > 0 {
			pdk.Log(pdk.LogInfo, fmt.Sprintf(
				"nd-rating-sync: user scan requested but cooldown active (%.0f min remaining)",
				remaining.Minutes()))
			return nil
		}
	}

	pdk.Log(pdk.LogInfo, "nd-rating-sync: running user-triggered rating sync")

	lastUserScanMu.Lock()
	lastUserScanTime = time.Now()
	lastUserScanMu.Unlock()

	return runSync()
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

// ─── Main sync logic ──────────────────────────────────────────────────────────

// runSync is the top-level entry called from the scheduler callback.
func runSync() error {
	cfg := loadConfig()
	if cfg.Username == "" {
		return errors.New("'username' is not configured – set it in the plugin settings")
	}

	songs, err := fetchAllSongs(cfg)
	if err != nil {
		return fmt.Errorf("fetching songs: %w", err)
	}

	rated, skipped, errored := 0, 0, 0
	for i, s := range songs {
		if cfg.MaxSongsPerRun > 0 && i >= cfg.MaxSongsPerRun {
			pdk.Log(pdk.LogInfo, fmt.Sprintf("nd-rating-sync: reached max_songs_per_run=%d, stopping", cfg.MaxSongsPerRun))
			break
		}

		if cfg.SkipAlreadyRated && s.UserRating > 0 {
			skipped++
			continue
		}

		stars, ok := extractStarsFromFile(s.Path, s.Suffix)
		if !ok {
			// No embedded rating tag found – nothing to do.
			skipped++
			continue
		}

		if err := setRating(cfg.Username, s.ID, stars); err != nil {
			pdk.Log(pdk.LogWarn, fmt.Sprintf(
				"nd-rating-sync: setRating failed for %q (id=%s): %v", s.Title, s.ID, err))
			errored++
			continue
		}

		pdk.Log(pdk.LogDebug, fmt.Sprintf(
			"nd-rating-sync: rated %q → %d stars", s.Title, stars))
		rated++
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: done – rated=%d skipped=%d errors=%d", rated, skipped, errored))
	return nil
}

// ─── Subsonic helpers ─────────────────────────────────────────────────────────

// fetchAllSongs pages through search3 and returns every song in the library.
func fetchAllSongs(cfg pluginConfig) ([]subsonicSong, error) {
	const pageSize = 500
	var all []subsonicSong
	offset := 0

	for {
		uri := fmt.Sprintf(
			"search3?query=%%22%%22&songCount=%d&songOffset=%d&albumCount=0&artistCount=0&u=%s",
			pageSize, offset, cfg.Username)

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

		if len(page) < pageSize {
			break // last page
		}
		offset += pageSize
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("nd-rating-sync: found %d songs in library", len(all)))
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

// ─── File-level tag dispatch ──────────────────────────────────────────────────

// extractStarsFromFile reads the audio file at path and returns a 1–5 star
// rating, or (0, false) if no recognised rating tag is found.
func extractStarsFromFile(path, suffix string) (int, bool) {
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
		return parseID3v2Rating(data)
	case "flac":
		return parseFlacVorbisRating(data)
	case "ogg", "oga":
		return parseOggVorbisRating(data)
	case "opus":
		return parseOggVorbisRating(data) // Opus uses the same Ogg container
	case "m4a", "mp4", "aac":
		return parseMP4Rating(data)
	default:
		// Attempt ID3v2 for any unrecognised format (e.g. AIFF with ID3 chunk).
		if stars, ok := parseID3v2Rating(data); ok {
			return stars, true
		}
		return 0, false
	}
}

// ─── Rating conversions ───────────────────────────────────────────────────────

// fmpsToStars converts an FMPS_Rating float string (0.0–1.0) to 1–5 stars.
// Returns (0, false) if the string cannot be parsed or is zero.
//
// Standard FMPS values map to stars via ceiling:
//
//	0.2 → 1 star, 0.4 → 2, 0.6 → 3, 0.8 → 4, 1.0 → 5
func fmpsToStars(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "0.0" {
		return 0, false
	}

	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, false
	}
	if f <= 0 {
		return 0, false
	}
	if f > 1 {
		f = 1
	}

	// Ceiling maps each fifth of the range to one star level so that the
	// canonical FMPS values (0.2, 0.4, 0.6, 0.8, 1.0) round correctly.
	stars := int(math.Ceil(f * 5))
	if stars > 5 {
		stars = 5
	}
	return stars, true
}

// popmToStars converts a POPM byte to 1–5 stars using the scale appropriate
// for the application identified by the email field.
//
// Different tag editors write POPM ratings on different byte scales:
//
//   - Windows Media Player ("Windows Media Player 9 Series"):
//     non-linear fixed points — 0 unrated, 1=★, 25=★★, 50=★★★, 75=★★★★, 99+=★★★★★
//
//   - Winamp, MediaMonkey, MusicBee, foobar2000, and most other editors:
//     percentile bands — 1-63=★, 64-127=★★, 128-191=★★★, 192-223=★★★★, 224-255=★★★★★
func popmToStars(email string, b byte) (int, bool) {
	if b == 0 {
		return 0, false
	}
	if strings.Contains(strings.ToLower(email), "windows media player") {
		return popmWMPToStars(b)
	}
	return popmWinampToStars(b)
}

// popmWMPToStars applies the Windows Media Player POPM scale.
// WMP fixed points: 0 unrated, 1=★, 25=★★, 50=★★★, 75=★★★★, 99+=★★★★★.
func popmWMPToStars(b byte) (int, bool) {
	switch {
	case b < 1:
		return 0, false
	case b < 25:
		return 1, true
	case b < 50:
		return 2, true
	case b < 75:
		return 3, true
	case b < 99:
		return 4, true
	default:
		return 5, true
	}
}

// popmWinampToStars applies the Winamp/MediaMonkey percentile band scale,
// which is the de-facto standard for all tag editors except Windows Media Player.
// 1-63=★, 64-127=★★, 128-191=★★★, 192-223=★★★★, 224-255=★★★★★.
func popmWinampToStars(b byte) (int, bool) {
	switch {
	case b < 64:
		return 1, true
	case b < 128:
		return 2, true
	case b < 192:
		return 3, true
	case b < 224:
		return 4, true
	default:
		return 5, true
	}
}
