package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withConfig swaps configValues for the duration of a test.
func withConfig(t *testing.T, values map[string]string) {
	t.Helper()
	old := configValues
	configValues = values
	t.Cleanup(func() { configValues = old })
}

func TestLoadConfig_Defaults(t *testing.T) {
	withConfig(t, map[string]string{})
	cfg := loadConfig()

	assert.Equal(t, "0 */6 * * *", cfg.SyncSchedule)
	assert.Equal(t, 24, cfg.UserScanCooldownHours)
	assert.Equal(t, 500, cfg.MaxSongsPerRun)
	assert.Empty(t, cfg.Libraries)
}

func TestLoadConfig_SyncSchedule(t *testing.T) {
	withConfig(t, map[string]string{"sync_schedule": "0 2 * * *"})
	cfg := loadConfig()
	assert.Equal(t, "0 2 * * *", cfg.SyncSchedule)
}

func TestLoadConfig_SyncScheduleWhitespaceOnlyUsesDefault(t *testing.T) {
	withConfig(t, map[string]string{"sync_schedule": "   "})
	cfg := loadConfig()
	assert.Equal(t, "0 */6 * * *", cfg.SyncSchedule)
}

func TestLoadConfig_CooldownHours(t *testing.T) {
	withConfig(t, map[string]string{"user_scan_cooldown_hours": "12"})
	cfg := loadConfig()
	assert.Equal(t, 12, cfg.UserScanCooldownHours)
}

func TestLoadConfig_CooldownZeroDisablesCooldown(t *testing.T) {
	withConfig(t, map[string]string{"user_scan_cooldown_hours": "0"})
	cfg := loadConfig()
	assert.Equal(t, 0, cfg.UserScanCooldownHours)
}

func TestLoadConfig_MaxSongsPerRun(t *testing.T) {
	withConfig(t, map[string]string{"max_songs_per_run": "250"})
	cfg := loadConfig()
	assert.Equal(t, 250, cfg.MaxSongsPerRun)
}

func TestLoadConfig_MaxSongsZeroMeansUnlimited(t *testing.T) {
	withConfig(t, map[string]string{"max_songs_per_run": "0"})
	cfg := loadConfig()
	assert.Equal(t, 0, cfg.MaxSongsPerRun)
}

func TestLoadConfig_MaxSongsInvalidKeepsDefault(t *testing.T) {
	withConfig(t, map[string]string{"max_songs_per_run": "not-a-number"})
	cfg := loadConfig()
	assert.Equal(t, 500, cfg.MaxSongsPerRun)
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

	withConfig(t, map[string]string{"libraries": string(raw)})
	cfg := loadConfig()

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
	withConfig(t, map[string]string{"libraries": string(raw)})

	cfg := loadConfig()
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
	withConfig(t, map[string]string{"libraries": string(raw)})

	cfg := loadConfig()
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
	withConfig(t, map[string]string{"libraries": string(raw)})

	cfg := loadConfig()
	assert.Equal(t, defaultTagOrder, cfg.Libraries[0].Users[0].RatingTagOrder)
}

func TestLoadConfig_InvalidLibrariesJSONKeepsEmptySlice(t *testing.T) {
	withConfig(t, map[string]string{"libraries": "not valid json"})
	cfg := loadConfig()
	assert.Empty(t, cfg.Libraries)
}

func TestLoadConfig_MultipleLibrariesAndUsers(t *testing.T) {
	libs := []map[string]any{
		{
			"libraryId": "lib1",
			"users":     []map[string]any{{"username": "alice"}, {"username": "bob"}},
		},
		{
			"libraryId": "lib2",
			"users":     []map[string]any{{"username": "carol"}},
		},
	}
	raw, _ := json.Marshal(libs)
	withConfig(t, map[string]string{"libraries": string(raw)})

	cfg := loadConfig()
	require.Len(t, cfg.Libraries, 2)
	assert.Len(t, cfg.Libraries[0].Users, 2)
	assert.Len(t, cfg.Libraries[1].Users, 1)
}
