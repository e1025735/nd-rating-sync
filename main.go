// nd-rating-sync – a Navidrome plugin that reads the embedded rating tag
// from each music file and writes it back into Navidrome via the Subsonic
// setRating API. Supported containers: MP3, FLAC, Ogg-Vorbis, Opus, WAV,
// DSF, M4A/AAC, and WMA. Supported rating sources (ratingTagOrder): WMP,
// iTunes, MediaMonkey, foobar2000, MusicBee and FMPS — see CLAUDE.md,
// "Supported tag formats", for the tag each one writes in each container.
//
// Build: tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
// Package: zip -j nd-rating-sync.ndp manifest.json plugin.wasm
package main

import (
	"fmt"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/e1025735/nd-rating-sync/internal/logging"
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
	logging.Info("nd-rating-sync: initialising")
	// A reload/restart kills any in-flight continuation chain. Mark it as
	// interrupted rather than deleting it: the immediate-on-load sweep is not
	// suppressed by a heartbeat from just before the restart, and it resumes
	// at the stored position instead of starting over (a reload is not a
	// crash, so without logging every file read).
	kvadapter.MarkSweepInterrupted("")
	purgeLegacyFileIndex()
	return registerSchedules(loadConfig())
}

// purgeLegacyFileIndex frees the KV space of the persistent file index that
// versions before the switch to Navidrome's matcher kept. Failure only costs
// space, so it is logged and ignored.
func purgeLegacyFileIndex() {
	deleted, err := kvadapter.PurgeLegacyFileIndex()
	switch {
	case err != nil:
		logging.Warn(fmt.Sprintf("nd-rating-sync: removing the old file index from the KV store failed: %q", err.Error()))
	case deleted > 0:
		logging.Info(fmt.Sprintf("nd-rating-sync: removed %d entries of the old file index from the KV store", deleted))
	}
}

// registerSchedules is the testable half of OnInit. It takes the resolved
// config and registers the two scheduler entries with the host.
func registerSchedules(cfg pluginConfig) error {
	cronExpr := cfg.SyncSchedule

	if _, err := host.SchedulerScheduleRecurring(cronExpr, "", scheduleID); err != nil {
		return fmt.Errorf("failed to register recurring scan: %w", err)
	}
	logging.Info(fmt.Sprintf("nd-rating-sync: scheduled recurring scan with cron %q", cronExpr))

	if _, err := host.SchedulerScheduleOneTime(0, "", scheduleIDImmediate); err != nil {
		return fmt.Errorf("failed to queue immediate scan: %w", err)
	}

	return nil
}

// ─── Scheduler callback ───────────────────────────────────────────────────────

func (ratingPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	logging.Info(fmt.Sprintf("nd-rating-sync: running scheduled rating sync (scheduleId=%q)", req.ScheduleID))
	return scanner.RunSyncStep(loadConfig(), req.Payload)
}
