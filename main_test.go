package main

import (
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetSchedulerMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SchedulerMock.ExpectedCalls = nil
		host.SchedulerMock.Calls = nil
	})
}

// ─── registerSchedules ────────────────────────────────────────────────────────

func TestRegisterSchedules_DefaultCron(t *testing.T) {
	resetSchedulerMock(t)
	cfg := pluginConfig{SyncSchedule: "0 * * * *"}

	host.SchedulerMock.On("ScheduleRecurring", "0 * * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)

	require.NoError(t, registerSchedules(cfg))
	host.SchedulerMock.AssertExpectations(t)
}

func TestRegisterSchedules_CustomCron(t *testing.T) {
	resetSchedulerMock(t)
	cfg := pluginConfig{SyncSchedule: "0 3 * * *"}

	host.SchedulerMock.On("ScheduleRecurring", "0 3 * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)

	require.NoError(t, registerSchedules(cfg))
	host.SchedulerMock.AssertExpectations(t)
}

func TestRegisterSchedules_RecurringFails(t *testing.T) {
	resetSchedulerMock(t)
	cfg := pluginConfig{SyncSchedule: "0 * * * *"}

	host.SchedulerMock.On("ScheduleRecurring", "0 * * * *", "", scheduleID).
		Return("", assert.AnError)

	require.Error(t, registerSchedules(cfg))
}

// ─── OnInit ───────────────────────────────────────────────────────────────────

// TestOnInit_ClearsSweepGuard proves OnInit drops any stale in-progress
// heartbeat (a reload kills the chain it belonged to) before registering the
// schedules, so the immediate-on-load sweep is never suppressed.
func TestOnInit_ClearsSweepGuard(t *testing.T) {
	resetSchedulerMock(t)
	resetKVStoreMock(t)

	host.KVStoreMock.On("Delete", "sweep-active").Return(nil)
	// loadConfig() returns the hourly default and no libraries in non-WASM builds.
	host.SchedulerMock.On("ScheduleRecurring", "0 * * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)

	require.NoError(t, ratingPlugin{}.OnInit())
	host.KVStoreMock.AssertExpectations(t)
	host.SchedulerMock.AssertExpectations(t)
}

// ─── OnCallback routing ──────────────────────────────────────────────────────
//
// OnCallback uses the production loadConfig which returns ("", false) for every
// key in non-WASM builds, so cfg.Libraries is empty. That's enough to verify
// the scheduleID → handler dispatch.

func TestOnCallback_RecurringRunsSyncAndErrorsWithoutLibraries(t *testing.T) {
	err := ratingPlugin{}.OnCallback(scheduler.SchedulerCallbackRequest{ScheduleID: scheduleID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

func TestOnCallback_ImmediateFallsThroughToSync(t *testing.T) {
	err := ratingPlugin{}.OnCallback(scheduler.SchedulerCallbackRequest{ScheduleID: scheduleIDImmediate})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

// TestOnCallback_UnknownIDsFallThroughToSync pins the forward-compat behaviour
// of the dispatch: any schedule ID other than the trigger-check sentinel —
// including a brand-new one the host might invent, an empty string, or the
// host-minted IDs used for sync continuations — must route to runSyncStep
// rather than be silently dropped. Without this guard, a future change to
// scheduler IDs in main.go could cause callbacks to vanish without anyone
// noticing.
func TestOnCallback_UnknownIDsFallThroughToSync(t *testing.T) {
	for _, id := range []string{"", "nd-rating-sync-some-future-id", "totally-foreign-id"} {
		t.Run(id, func(t *testing.T) {
			err := ratingPlugin{}.OnCallback(scheduler.SchedulerCallbackRequest{ScheduleID: id})
			require.Error(t, err, "ID %q should route to runSyncStep (which errors on empty libraries)", id)
			assert.Contains(t, err.Error(), "no libraries configured",
				"ID %q must route to runSyncStep, not be silently dropped", id)
		})
	}
}

// ─── schedule ID constants ────────────────────────────────────────────────────

func TestScheduleIDConstants(t *testing.T) {
	assert.Equal(t, "nd-rating-sync-recurring", scheduleID)
	assert.Equal(t, "nd-rating-sync-immediate", scheduleIDImmediate)
}