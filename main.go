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
	"errors"
	"fmt"
	"time"

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
	clearSweepActive()
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
	return runSyncStep(loadConfig(), req.Payload)
}

// runSyncStep runs one budgeted slice of a sync. payload is the scheduler
// callback payload: empty for a fresh full sweep, or a serialised syncCursor
// for a continuation. When the slice exhausts its time budget before the sweep
// finishes, it persists the cursor into a fresh one-time callback so the work
// resumes almost immediately in a new call — keeping every individual call
// comfortably under the host's hardcoded 30s plugin-call limit.
//
// The continuation uses an empty scheduleID so the host mints a unique one:
// the currently-firing one-time entry is still registered during its own
// callback, so reusing its ID would be rejected as a duplicate.
func runSyncStep(cfg pluginConfig, payload string) error {
	return runSyncStepUntil(cfg, payload, time.Now().Add(callBudget))
}

// runSyncStepUntil is the deadline-injectable core of runSyncStep, split out so
// tests can force the budget-reached/continuation path without waiting 20s.
//
// Every sweep records an in-progress heartbeat so a freshly-triggered sweep
// (e.g. the hourly cron firing while a big first import is still chaining) does
// not start a second concurrent sweep.
func runSyncStepUntil(cfg pluginConfig, payload string, deadline time.Time) error {
	if len(cfg.Libraries) == 0 {
		return errors.New("no libraries configured – add at least one library with users in the plugin settings")
	}

	cur, resumed := parseCursor(payload)

	if !resumed && sweepInProgress() {
		logInfo("nd-rating-sync: a sweep is already in progress – skipping this trigger")
		return nil
	}
	markSweepActive() // set on a fresh start; refresh on every continuation

	if resumed {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: resuming sync at library#%d user#%d offset=%d",
			cur.Lib, cur.User, cur.Offset))
	} else {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: starting sync – libraries=%d incremental=%v dry_run=%v budget=%s",
			len(cfg.Libraries), cfg.IncrementalSync, cfg.DryRun, callBudget))
	}

	next, done := runSyncChunk(cfg, cur, deadline)
	if done {
		clearSweepActive()
		logInfo("nd-rating-sync: sync complete")
		return nil
	}

	if _, err := host.SchedulerScheduleOneTime(0, next.marshal(), ""); err != nil {
		return fmt.Errorf("failed to reschedule sync continuation: %w", err)
	}
	logInfo(fmt.Sprintf(
		"nd-rating-sync: time budget (%s) reached – rescheduled continuation at library#%d user#%d offset=%d",
		callBudget, next.Lib, next.User, next.Offset))
	return nil
}