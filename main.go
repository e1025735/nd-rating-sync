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

	// Schedule a recurring scan every 6 hours (cron syntax).
	if err := host.SchedulerAddRecurring(scheduleID, "0 */6 * * *", nil); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: failed to register recurring scan: "+err.Error())
		return 1
	}

	// Also queue an immediate one-shot run so the first scan happens right away
	// (the recurring job would fire after the first 6-hour window otherwise).
	if err := host.SchedulerAddOnce(scheduleIDImmediate, 0, nil); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: failed to queue immediate scan: "+err.Error())
		return 1
	}

	pdk.Log(pdk.LogInfo, "nd-rating-sync: scheduled recurring scan every 6 hours")
	return 0
}

// ─── Scheduler callback ───────────────────────────────────────────────────────

const (
	scheduleID          = "nd-rating-sync-recurring"
	scheduleIDImmediate = "nd-rating-sync-immediate"
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

	pdk.Log(pdk.LogInfo, "nd-rating-sync: running rating sync (scheduleId="+input.ScheduleID+")")

	if err := runSync(); err != nil {
		pdk.Log(pdk.LogError, "nd-rating-sync: sync failed: "+err.Error())
		return 1
	}
	return 0
}
