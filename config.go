package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

type pluginConfig struct {
	SyncSchedule          string
	UserScanCooldownHours int
	MaxSongsPerRun        int
	Libraries             []libraryConfig
}

type jsonUserConfig struct {
	Username         string   `json:"username"`
	TriggerUserScan  bool     `json:"trigger_user_scan"`
	SkipAlreadyRated *bool    `json:"skip_already_rated"` // pointer so absence → default true
	RatingTagOrder   []string `json:"ratingTagOrder"`
}

type jsonLibraryConfig struct {
	LibraryID   string           `json:"libraryId"`
	LibraryName string           `json:"libraryName"`
	Users       []jsonUserConfig `json:"users"`
}

var defaultTagOrder = []string{"WMP", "iTunes", "MediaMonkey"}

// configGetter abstracts how a single config key is fetched. Production passes
// the real PDK getConfig; tests pass a closure over a local map.
type configGetter func(key string) (string, bool)

// loadConfig reads from the real PDK config store. It is the production entry
// point; tests should call loadConfigFrom directly with their own getter.
func loadConfig() pluginConfig { return loadConfigFrom(getConfig) }

func loadConfigFrom(get configGetter) pluginConfig {
	cfg := pluginConfig{
		SyncSchedule:          "0 */6 * * *",
		UserScanCooldownHours: 24,
		MaxSongsPerRun:        500,
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
	if v, ok := get("max_songs_per_run"); ok {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 0 {
			cfg.MaxSongsPerRun = n
		} else {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: invalid max_songs_per_run=%q – using default %d", v, cfg.MaxSongsPerRun))
		}
	}

	if v, ok := get("libraries"); ok && v != "" {
		var rawLibs []jsonLibraryConfig
		if err := json.Unmarshal([]byte(v), &rawLibs); err != nil {
			logWarn("nd-rating-sync: failed to parse libraries config: " + err.Error())
		} else {
			for _, rl := range rawLibs {
				lc := libraryConfig{LibraryID: rl.LibraryID, LibraryName: rl.LibraryName}
				for _, ru := range rl.Users {
					uc := userConfig{
						Username:         ru.Username,
						TriggerUserScan:  ru.TriggerUserScan,
						SkipAlreadyRated: true,
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

	logDebug(fmt.Sprintf(
		"nd-rating-sync: config – libraries=%d sync_schedule=%q max_songs_per_run=%d",
		len(cfg.Libraries), cfg.SyncSchedule, cfg.MaxSongsPerRun))
	return cfg
}