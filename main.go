// nd-rating-sync – a Navidrome plugin that reads the embedded rating tag
// from each music file (FMPS_Rating / POPM for ID3v2) and writes it back
// into Navidrome via the Subsonic setRating API.
//
// Build: tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
// Package: zip -j nd-rating-sync.ndp manifest.json plugin.wasm
package main

import (
	"fmt"

	pdk "github.com/extism/go-pdk"
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
	scheduleID             = "nd-rating-sync-recurring"
	scheduleIDImmediate    = "nd-rating-sync-immediate"
	scheduleIDTriggerCheck = "nd-rating-sync-trigger-check"
)

// ─── Lifecycle ────────────────────────────────────────────────────────────────

func (ratingPlugin) OnInit() error {
	pdk.Log(pdk.LogInfo, "nd-rating-sync: initialising")

	cfg := loadConfig()
	cronExpr := cfg.SyncSchedule

	if _, err := host.SchedulerScheduleRecurring(cronExpr, "", scheduleID); err != nil {
		return fmt.Errorf("failed to register recurring scan: %w", err)
	}
	pdk.Log(pdk.LogInfo, "nd-rating-sync: scheduled recurring scan with cron '"+cronExpr+"'")

	if _, err := host.SchedulerScheduleOneTime(0, "", scheduleIDImmediate); err != nil {
		return fmt.Errorf("failed to queue immediate scan: %w", err)
	}

	if _, err := host.SchedulerScheduleRecurring("*/15 * * * *", "", scheduleIDTriggerCheck); err != nil {
		return fmt.Errorf("failed to register trigger-check: %w", err)
	}
	pdk.Log(pdk.LogInfo, "nd-rating-sync: registered user-trigger check (every 15 min)")

	return nil
}

// ─── Scheduler callback ───────────────────────────────────────────────────────

func (ratingPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	switch req.ScheduleID {
	case scheduleIDTriggerCheck:
		return checkAndRunUserTriggeredScan()
	default:
		pdk.Log(pdk.LogInfo, "nd-rating-sync: running scheduled rating sync (scheduleId="+req.ScheduleID+")")
		return runSync()
	}
}
