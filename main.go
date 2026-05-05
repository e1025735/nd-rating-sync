// nd-rating-sync – a Navidrome plugin that reads the embedded rating tag
// from each music file (FMPS_Rating / POPM for ID3v2; FMPS_RATING for Vorbis
// comments) and writes it back into Navidrome via the Subsonic setRating API.
//
// Build: tinygo build -o plugin.wasm -target wasip1 -buildmode=c-shared .
// Package: zip -j nd-rating-sync.ndp manifest.json plugin.wasm
package main

import (
	"encoding/json"

	pdk "github.com/extism/go-pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// ─── Capability registrations ────────────────────────────────────────────────
// These init()-time calls tell Navidrome which capabilities this plugin
// provides.  They must happen before any exported function runs.

func init() {
	// Lifecycle: we want OnInit to schedule the first scan.
	host.RegisterLifecycle()
	// SchedulerCallback: we handle our own cron events.
	host.RegisterSchedulerCallback()
}

func main() {}

// ─── Lifecycle ────────────────────────────────────────────────────────────────

// nd_lifecycle_on_init is called once when the plugin is fully loaded.
//
//go:wasmexport nd_lifecycle_on_init
func ndLifecycleOnInit() int32 {
	pdk.Log(pdk.LogInfo, "nd-rating-sync: initialising")

	cfg := loadConfig()

	// Schedule recurring scans using the admin-configured cron expression.
	cronExpr := cfg.SyncSchedule
	if err := host.SchedulerAddRecurring(scheduleID, cronExpr, nil); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: failed to register recurring scan: "+err.Error())
		return 1
	}
	pdk.Log(pdk.LogInfo, "nd-rating-sync: scheduled recurring scan with cron '"+cronExpr+"'")

	// Queue an immediate one-shot run so the first scan happens right away
	// (the recurring job would fire after the first cron window otherwise).
	if err := host.SchedulerAddOnce(scheduleIDImmediate, 0, nil); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: failed to queue immediate scan: "+err.Error())
		return 1
	}

	// Schedule a frequent check that picks up user-triggered scan requests.
	// Users set trigger_user_scan=true in the plugin config; this job polls for it.
	if err := host.SchedulerAddRecurring(scheduleIDTriggerCheck, "*/15 * * * *", nil); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: failed to register trigger-check: "+err.Error())
		return 1
	}
	pdk.Log(pdk.LogInfo, "nd-rating-sync: registered user-trigger check (every 15 min)")

	return 0
}

// ─── Scheduler callback ───────────────────────────────────────────────────────

const (
	scheduleID             = "nd-rating-sync-recurring"
	scheduleIDImmediate    = "nd-rating-sync-immediate"
	scheduleIDTriggerCheck = "nd-rating-sync-trigger-check"
)

// schedulerCallbackInput mirrors the JSON that Navidrome passes to the
// nd_scheduler_callback export.
type schedulerCallbackInput struct {
	ScheduleID  string `json:"scheduleId"`
	IsRecurring bool   `json:"isRecurring"`
}

// nd_scheduler_callback is invoked by Navidrome when a scheduled job fires.
//
//go:wasmexport nd_scheduler_callback
func ndSchedulerCallback() int32 {
	var input schedulerCallbackInput
	if err := json.Unmarshal(pdk.Input(), &input); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: bad scheduler payload: "+err.Error())
		return 1
	}

	switch input.ScheduleID {
	case scheduleIDTriggerCheck:
		// Poll for a user-requested scan; does nothing if no request is pending
		// or the cooldown has not elapsed yet.
		if err := checkAndRunUserTriggeredScan(); err != nil {
			pdk.Log(pdk.LogError, "nd-rating-sync: user-triggered scan failed: "+err.Error())
			return 1
		}
	default:
		pdk.Log(pdk.LogInfo, "nd-rating-sync: running scheduled rating sync (scheduleId="+input.ScheduleID+")")
		if err := runSync(); err != nil {
			pdk.Log(pdk.LogError, "nd-rating-sync: sync failed: "+err.Error())
			return 1
		}
	}
	return 0
}
