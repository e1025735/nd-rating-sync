package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type userConfig struct {
	Username              string
	TriggerUserScan       bool
	SkipAlreadyRated      bool
	ClearRatingIfUntagged bool
	RatingTagOrder        []string
}

type libraryConfig struct {
	LibraryID   string
	LibraryName string
	Users       []userConfig
}

type pluginConfig struct {
	SyncSchedule                 string
	UserScanCooldownHours        int
	MaxSongsPerRun               int
	IncrementalSync              bool
	DryRun                       bool
	DefaultTriggerUserScan       *bool
	DefaultSkipAlreadyRated      *bool
	DefaultClearRatingIfUntagged *bool
	Libraries                    []libraryConfig
}

type jsonUserConfig struct {
	Username              string   `json:"username"`
	TriggerUserScan       string   `json:"trigger_user_scan"`
	SkipAlreadyRated      string   `json:"skip_already_rated"`
	ClearRatingIfUntagged string   `json:"clear_rating_if_untagged"`
	RatingTagOrder        []string `json:"ratingTagOrder"`
}

type jsonLibraryConfig struct {
	LibraryID   string           `json:"libraryId"`
	LibraryName string           `json:"libraryName"`
	Users       []jsonUserConfig `json:"users"`
}

var defaultTagOrder = []string{"WMP", "iTunes", "MediaMonkey", "foobar2000"}

// parseTristateConfig parses a PDK string value into a tristate bool pointer.
// "true"/"1"/"yes"/"on" → &true; "false"/"0"/"no"/"off" → &false; anything else → nil (not set).
func parseTristateConfig(v string) *bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		b := true
		return &b
	case "false", "0", "no", "off":
		b := false
		return &b
	default:
		return nil
	}
}

// resolveTristate returns the first non-nil value in: user → admin → plugin hardcoded fallback.
func resolveTristate(user, admin *bool, fallback bool) bool {
	if user != nil {
		return *user
	}
	if admin != nil {
		return *admin
	}
	return fallback
}

// configGetter abstracts how a single config key is fetched. Production passes
// the real PDK getConfig; tests pass a closure over a local map.
type configGetter func(key string) (string, bool)

// loadConfig reads from the real PDK config store. It is the production entry
// point; tests should call loadConfigFrom directly with their own getter.
func loadConfig() pluginConfig { return loadConfigFrom(getConfig) }

func loadConfigFrom(get configGetter) pluginConfig {
	cfg := pluginConfig{
		SyncSchedule:          "0 * * * *",
		UserScanCooldownHours: 24,
		IncrementalSync:       true,
	}

	if v, ok := get("sync_schedule"); ok {
		if s := strings.TrimSpace(v); s != "" {
			cfg.SyncSchedule = s
		}
	}
	if v, ok := get("user_scan_cooldown_hours"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			cfg.UserScanCooldownHours = n
		}
	}
	if v, ok := get("incremental_sync"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "false", "0", "no", "off":
			cfg.IncrementalSync = false
		case "true", "1", "yes", "on", "":
			// keep default true (or explicit true)
		default:
			logWarn(fmt.Sprintf(
				"nd-rating-sync: invalid incremental_sync=%q – keeping default %v", v, cfg.IncrementalSync))
		}
	}
	if v, ok := get("dry_run"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			cfg.DryRun = true
		}
	}
	if v, ok := get("default_trigger_user_scan"); ok {
		cfg.DefaultTriggerUserScan = parseTristateConfig(v)
	}
	if v, ok := get("default_skip_already_rated"); ok {
		cfg.DefaultSkipAlreadyRated = parseTristateConfig(v)
	}
	if v, ok := get("default_clear_rating_if_untagged"); ok {
		cfg.DefaultClearRatingIfUntagged = parseTristateConfig(v)
	}
	if v, ok := get("libraries"); ok && v != "" {
		var rawLibs []jsonLibraryConfig
		if err := json.Unmarshal([]byte(v), &rawLibs); err != nil {
			// %q on err.Error() so a crafted config blob can't smuggle CR/LF
			// or ANSI escapes through the json.Unmarshal echo into log sinks.
			logWarn(fmt.Sprintf("nd-rating-sync: failed to parse libraries config: %q", err.Error()))
		} else {
			for _, rl := range rawLibs {
				lc := libraryConfig{LibraryID: rl.LibraryID, LibraryName: rl.LibraryName}
				for _, ru := range rl.Users {
					uc := userConfig{
						Username:              ru.Username,
						TriggerUserScan:       resolveTristate(parseTristateConfig(ru.TriggerUserScan), cfg.DefaultTriggerUserScan, false),
						SkipAlreadyRated:      resolveTristate(parseTristateConfig(ru.SkipAlreadyRated), cfg.DefaultSkipAlreadyRated, true),
						ClearRatingIfUntagged: resolveTristate(parseTristateConfig(ru.ClearRatingIfUntagged), cfg.DefaultClearRatingIfUntagged, false),
						RatingTagOrder:        ru.RatingTagOrder,
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

	logDebug(fmt.Sprintf(
		"nd-rating-sync: config – libraries=%d sync_schedule=%q incremental=%v",
		len(cfg.Libraries), cfg.SyncSchedule, cfg.IncrementalSync))
	return cfg
}
