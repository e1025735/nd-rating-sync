package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_Defaults(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{}))

	assert.Equal(t, "0 * * * *", cfg.SyncSchedule)
	assert.Equal(t, 24, cfg.UserScanCooldownHours)
	assert.True(t, cfg.IncrementalSync, "IncrementalSync should default to true")
	assert.Empty(t, cfg.Libraries)
}

func TestLoadConfig_IncrementalSync(t *testing.T) {
	for _, falsy := range []string{"false", "FALSE", "0", "no", "off"} {
		cfg := loadConfigFrom(mapGetter(map[string]string{"incremental_sync": falsy}))
		assert.False(t, cfg.IncrementalSync, "incremental_sync=%q should disable", falsy)
	}
	for _, truthy := range []string{"true", "1", "yes", "on", ""} {
		cfg := loadConfigFrom(mapGetter(map[string]string{"incremental_sync": truthy}))
		assert.True(t, cfg.IncrementalSync, "incremental_sync=%q should keep default", truthy)
	}
}

func TestLoadConfig_IncrementalSyncInvalidKeepsDefault(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{"incremental_sync": "maybe"}))
	assert.True(t, cfg.IncrementalSync)
}

func TestLoadConfig_SyncSchedule(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{"sync_schedule": "0 2 * * *"}))
	assert.Equal(t, "0 2 * * *", cfg.SyncSchedule)
}

func TestLoadConfig_SyncScheduleWhitespaceOnlyUsesDefault(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{"sync_schedule": "   "}))
	assert.Equal(t, "0 * * * *", cfg.SyncSchedule)
}

func TestLoadConfig_CooldownHours(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{"user_scan_cooldown_hours": "12"}))
	assert.Equal(t, 12, cfg.UserScanCooldownHours)
}

func TestLoadConfig_CooldownZeroDisablesCooldown(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{"user_scan_cooldown_hours": "0"}))
	assert.Equal(t, 0, cfg.UserScanCooldownHours)
}

func TestLoadConfig_Libraries(t *testing.T) {
	libs := []map[string]any{
		{
			"libraryId":   "lib1",
			"libraryName": "Main Library",
			"users": []map[string]any{
				{"username": "alice", "ratingTagOrder": []string{"WMP", "iTunes"}},
			},
		},
	}
	raw, err := json.Marshal(libs)
	require.NoError(t, err)

	cfg := loadConfigFrom(mapGetter(map[string]string{"libraries": string(raw)}))

	require.Len(t, cfg.Libraries, 1)
	lib := cfg.Libraries[0]
	assert.Equal(t, "lib1", lib.LibraryID)
	assert.Equal(t, "Main Library", lib.LibraryName)

	require.Len(t, lib.Users, 1)
	u := lib.Users[0]
	assert.Equal(t, "alice", u.Username)
	assert.Equal(t, []string{"WMP", "iTunes"}, u.RatingTagOrder)
}

func TestLoadConfig_SkipAlreadyRatedDefaultsTrue(t *testing.T) {
	libs := []map[string]any{
		{"libraryId": "lib1", "users": []map[string]any{{"username": "bob"}}},
	}
	raw, _ := json.Marshal(libs)
	cfg := loadConfigFrom(mapGetter(map[string]string{"libraries": string(raw)}))
	assert.True(t, cfg.Libraries[0].Users[0].SkipAlreadyRated)
}

func TestLoadConfig_SkipAlreadyRatedCanBeDisabled(t *testing.T) {
	falseVal := false
	libs := []map[string]any{
		{
			"libraryId": "lib1",
			"users":     []map[string]any{{"username": "carol", "skip_already_rated": falseVal}},
		},
	}
	raw, _ := json.Marshal(libs)
	cfg := loadConfigFrom(mapGetter(map[string]string{"libraries": string(raw)}))
	assert.False(t, cfg.Libraries[0].Users[0].SkipAlreadyRated)
}

func TestLoadConfig_TagOrderDefaultWhenEmpty(t *testing.T) {
	libs := []map[string]any{
		{
			"libraryId": "lib1",
			"users":     []map[string]any{{"username": "dave", "ratingTagOrder": []string{}}},
		},
	}
	raw, _ := json.Marshal(libs)
	cfg := loadConfigFrom(mapGetter(map[string]string{"libraries": string(raw)}))
	assert.Equal(t, defaultTagOrder, cfg.Libraries[0].Users[0].RatingTagOrder)
}

func TestLoadConfig_InvalidLibrariesJSONKeepsEmptySlice(t *testing.T) {
	cfg := loadConfigFrom(mapGetter(map[string]string{"libraries": "not valid json"}))
	assert.Empty(t, cfg.Libraries)
}

func TestLoadConfig_MultipleLibrariesAndUsers(t *testing.T) {
	libs := []map[string]any{
		{"libraryId": "lib1", "users": []map[string]any{{"username": "alice"}, {"username": "bob"}}},
		{"libraryId": "lib2", "users": []map[string]any{{"username": "carol"}}},
	}
	raw, _ := json.Marshal(libs)
	cfg := loadConfigFrom(mapGetter(map[string]string{"libraries": string(raw)}))

	require.Len(t, cfg.Libraries, 2)
	assert.Len(t, cfg.Libraries[0].Users, 2)
	assert.Len(t, cfg.Libraries[1].Users, 1)
}

func TestLoadConfigFrom_IsParallelSafe(t *testing.T) {
	// Two parallel subtests with disjoint configs prove there is no global state.
	t.Run("a", func(t *testing.T) {
		t.Parallel()
		cfg := loadConfigFrom(mapGetter(map[string]string{"sync_schedule": "0 1 * * *"}))
		assert.Equal(t, "0 1 * * *", cfg.SyncSchedule)
	})
	t.Run("b", func(t *testing.T) {
		t.Parallel()
		cfg := loadConfigFrom(mapGetter(map[string]string{"sync_schedule": "0 2 * * *"}))
		assert.Equal(t, "0 2 * * *", cfg.SyncSchedule)
	})
}