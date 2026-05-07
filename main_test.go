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
	cfg := pluginConfig{SyncSchedule: "0 */6 * * *"}

	host.SchedulerMock.On("ScheduleRecurring", "0 */6 * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)
	host.SchedulerMock.On("ScheduleRecurring", "*/15 * * * *", "", scheduleIDTriggerCheck).Return("id-3", nil)

	require.NoError(t, registerSchedules(cfg))
	host.SchedulerMock.AssertExpectations(t)
}

func TestRegisterSchedules_CustomCron(t *testing.T) {
	resetSchedulerMock(t)
	cfg := pluginConfig{SyncSchedule: "0 3 * * *"}

	host.SchedulerMock.On("ScheduleRecurring", "0 3 * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)
	host.SchedulerMock.On("ScheduleRecurring", "*/15 * * * *", "", scheduleIDTriggerCheck).Return("id-3", nil)

	require.NoError(t, registerSchedules(cfg))
	host.SchedulerMock.AssertExpectations(t)
}

func TestRegisterSchedules_RecurringFails(t *testing.T) {
	resetSchedulerMock(t)
	cfg := pluginConfig{SyncSchedule: "0 */6 * * *"}

	host.SchedulerMock.On("ScheduleRecurring", "0 */6 * * *", "", scheduleID).
		Return("", assert.AnError)

	require.Error(t, registerSchedules(cfg))
}

// ─── OnCallback routing ──────────────────────────────────────────────────────
//
// OnCallback uses the production loadConfig which returns ("", false) for every
// key in non-WASM builds, so cfg.Libraries is empty. That's enough to verify
// the scheduleID → handler dispatch.

func TestOnCallback_TriggerCheckIsNoOp(t *testing.T) {
	resetSubsonicMock(t)
	err := ratingPlugin{}.OnCallback(scheduler.SchedulerCallbackRequest{ScheduleID: scheduleIDTriggerCheck})
	assert.NoError(t, err)
	host.SubsonicAPIMock.AssertNotCalled(t, "Call")
}

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

// ─── schedule ID constants ────────────────────────────────────────────────────

func TestScheduleIDConstants(t *testing.T) {
	assert.Equal(t, "nd-rating-sync-recurring", scheduleID)
	assert.Equal(t, "nd-rating-sync-immediate", scheduleIDImmediate)
	assert.Equal(t, "nd-rating-sync-trigger-check", scheduleIDTriggerCheck)
}