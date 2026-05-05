package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// ─── Configuration ────────────────────────────────────────────────────────────

// pluginConfig holds values the admin sets in the Navidrome plugin UI.
type pluginConfig struct {
	// Username of the Navidrome account used for API calls.
	// Required – the Subsonic API always needs a `u` parameter.
	Username string
	// SkipAlreadyRated controls whether songs that already have a user rating
	// in Navidrome are left untouched.  Defaults to true.
	SkipAlreadyRated bool
	// MaxSongsPerRun caps the number of songs processed per scheduler run to
	// avoid very long-running tasks on large libraries.  0 = unlimited.
	MaxSongsPerRun int
}

func loadConfig() pluginConfig {
	cfg := pluginConfig{
		SkipAlreadyRated: true,
		MaxSongsPerRun:   500,
	}
	if v, ok := pdk.GetConfig("username"); ok {
		cfg.Username = strings.TrimSpace(v)
	}
	if v, ok := pdk.GetConfig("skip_already_rated"); ok {
		cfg.SkipAlreadyRated = strings.ToLower(strings.TrimSpace(v)) != "false"
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
	pdk.Log(pdk.LogDebug, fmt.Sprintf(
		"nd-rating-sync: config – username=%q skip_already_rated=%v max_songs_per_run=%d",
		cfg.Username, cfg.SkipAlreadyRated, cfg.MaxSongsPerRun))
	return cfg
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

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: starting sync – skip_already_rated=%v max_songs_per_run=%d",
		cfg.SkipAlreadyRated, cfg.MaxSongsPerRun))

	songs, err := fetchAllSongs(cfg)
	if err != nil {
		return fmt.Errorf("fetching songs: %w", err)
	}

	rated, skippedRated, skippedNoTag, errored := 0, 0, 0, 0
	for i, s := range songs {
		if cfg.MaxSongsPerRun > 0 && i >= cfg.MaxSongsPerRun {
			pdk.Log(pdk.LogInfo, fmt.Sprintf(
				"nd-rating-sync: reached max_songs_per_run=%d, stopping early", cfg.MaxSongsPerRun))
			break
		}

		if cfg.SkipAlreadyRated && s.UserRating > 0 {
			pdk.Log(pdk.LogDebug, fmt.Sprintf(
				"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
			skippedRated++
			continue
		}

		stars, ok := extractStarsFromFile(s.Path, s.Suffix)
		if !ok {
			skippedNoTag++
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
		"nd-rating-sync: done – rated=%d skipped_already_rated=%d skipped_no_tag=%d errors=%d",
		rated, skippedRated, skippedNoTag, errored))
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

		pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: fetching songs – offset=%d page_size=%d", offset, pageSize))

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
		pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: page offset=%d returned %d songs (total so far: %d)", offset, len(page), len(all)))

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
		stars, ok := parseID3v2Rating(data)
		if ok {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		} else {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
		}
		return stars, ok
	default:
		pdk.Log(pdk.LogWarn, fmt.Sprintf("nd-rating-sync: skipping %q – only MP3 files are supported (got .%s)", path, ext))
		return 0, false
	}
}

// ─── Rating conversions ───────────────────────────────────────────────────────

// fmpsToStars converts an FMPS_Rating float string (0.0–1.0) to 1–5 stars.
// Returns (0, false) if the string cannot be parsed or is zero.
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

	// Map 0.0–1.0 to 1–5 using equal bands.
	stars := int(f*5) + 1
	if stars > 5 {
		stars = 5
	}
	return stars, true
}

// popmToStars converts a POPM byte (0–255) to 1–5 stars using the
// Winamp/MediaMonkey de-facto standard mapping.
//
//	0        → unrated
//	1–63     → 1 star
//	64–127   → 2 stars
//	128–191  → 3 stars
//	192–223  → 4 stars
//	224–255  → 5 stars
func popmToStars(b byte) (int, bool) {
	switch {
	case b == 0:
		return 0, false
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
