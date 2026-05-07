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

// ─── OnInit ───────────────────────────────────────────────────────────────────

func TestOnInit_RegistersAllThreeSchedules(t *testing.T) {
	resetSchedulerMock(t)
	withConfig(t, map[string]string{})

	host.SchedulerMock.On("ScheduleRecurring", "0 */6 * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)
	host.SchedulerMock.On("ScheduleRecurring", "*/15 * * * *", "", scheduleIDTriggerCheck).Return("id-3", nil)

	err := ratingPlugin{}.OnInit()

	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
}

func TestOnInit_UsesConfiguredCronExpression(t *testing.T) {
	resetSchedulerMock(t)
	withConfig(t, map[string]string{"sync_schedule": "0 3 * * *"})

	host.SchedulerMock.On("ScheduleRecurring", "0 3 * * *", "", scheduleID).Return("id-1", nil)
	host.SchedulerMock.On("ScheduleOneTime", int32(0), "", scheduleIDImmediate).Return("id-2", nil)
	host.SchedulerMock.On("ScheduleRecurring", "*/15 * * * *", "", scheduleIDTriggerCheck).Return("id-3", nil)

	err := ratingPlugin{}.OnInit()
	require.NoError(t, err)
	host.SchedulerMock.AssertExpectations(t)
}

func TestOnInit_ReturnsErrorWhenScheduleFails(t *testing.T) {
	resetSchedulerMock(t)
	withConfig(t, map[string]string{})

	host.SchedulerMock.On("ScheduleRecurring", "0 */6 * * *", "", scheduleID).
		Return("", assert.AnError)

	err := ratingPlugin{}.OnInit()
	require.Error(t, err)
}

// ─── OnCallback ──────────────────────────────────────────────────────────────

func TestOnCallback_TriggerCheckID_NoUsersConfigured(t *testing.T) {
	withConfig(t, map[string]string{})
	err := ratingPlugin{}.OnCallback(scheduler.SchedulerCallbackRequest{ScheduleID: scheduleIDTriggerCheck})
	assert.NoError(t, err)
}

func TestOnCallback_RecurringID_NoLibrariesReturnsError(t *testing.T) {
	withConfig(t, map[string]string{})
	err := ratingPlugin{}.OnCallback(scheduler.SchedulerCallbackRequest{ScheduleID: scheduleID})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no libraries configured")
}

func TestOnCallback_ImmediateID_FallsThroughToSync(t *testing.T) {
	withConfig(t, map[string]string{})
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
