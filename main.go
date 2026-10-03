// nd-rating-sync – a Navidrome plugin that reads the embedded rating tag
// from each music file and writes it back into Navidrome via the Subsonic
// setRating API. Supported containers: MP3, FLAC, Ogg-Vorbis, Opus, WAV,
// DSF, M4A/AAC, and WMA. Supported tag sources: MediaMonkey FMPS_Rating,
// foobar2000 RATING, WMP POPM / WM/SharedUserRating, iTunes POPM / lowercase
// "rating" atom.
//
// Build: tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
// Package: zip -j nd-rating-sync.ndp manifest.json plugin.wasm
package main

import (
	"fmt"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	scanner "github.com/e1025735/nd-rating-sync/internal/scanner"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/lifecycle"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
)

// ─── Capability registrations ────────────────────────────────────────────────

type ratingPlugin struct{}

func init() {
	p := ratingPlugin{}
	lifecycle.Register(p)
	scheduler.Register(p)
}

func main() {}

// ─── Scheduler IDs ────────────────────────────────────────────────────────────

const (
	scheduleID          = "nd-rating-sync-recurring"
	scheduleIDImmediate = "nd-rating-sync-immediate"
)

// ─── Lifecycle ────────────────────────────────────────────────────────────────

func (ratingPlugin) OnInit() error {
	logInfo("nd-rating-sync: initialising")
	// A reload/restart kills any in-flight continuation chain, so clear the
	// in-progress guard up front — a heartbeat left over from just before the
	// restart must not suppress the immediate-on-load sweep until it goes stale.
	kvadapter.ClearSweepActive()
	kvadapter.EnsureConfigCacheIsCurrent(loadConfig())
	return registerSchedules(loadConfig())
}

// registerSchedules is the testable half of OnInit. It takes the resolved
// config and registers the two scheduler entries with the host.
func registerSchedules(cfg pluginConfig) error {
	cronExpr := cfg.SyncSchedule

	if _, err := host.SchedulerScheduleRecurring(cronExpr, "", scheduleID); err != nil {
		return fmt.Errorf("failed to register recurring scan: %w", err)
	}
	logInfo(fmt.Sprintf("nd-rating-sync: scheduled recurring scan with cron %q", cronExpr))

	if _, err := host.SchedulerScheduleOneTime(0, "", scheduleIDImmediate); err != nil {
		return fmt.Errorf("failed to queue immediate scan: %w", err)
	}

	return nil
}

// ─── Scheduler callback ───────────────────────────────────────────────────────

func (ratingPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	logInfo(fmt.Sprintf("nd-rating-sync: running scheduled rating sync (scheduleId=%q)", req.ScheduleID))
	return scanner.RunSyncStep(toScannerConfig(loadConfig()), req.Payload)
}
