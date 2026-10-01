package scanner

import (
	"encoding/json"
	"fmt"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

type UserConfig struct {
	Username              string
	SkipAlreadyRated      bool
	ClearRatingIfUntagged bool
	RatingTagOrder        []string
}

type LibraryConfig struct {
	LibraryID   string
	LibraryName string
	Users       []UserConfig
}

type PluginConfig struct {
	SyncSchedule                 string
	MaxSongsPerRun               int
	IncrementalSync              bool
	DryRun                       bool
	CacheLibrariesFilesystemTree bool
	DefaultSkipAlreadyRated      *bool
	DefaultClearRatingIfUntagged *bool
	KVStorageMaxSize             string
	Libraries                    []LibraryConfig
}

type FileEntry struct {
	Path  string
	Size  int64
	MTime time.Time
}

type SyncCursor struct {
	Lib       int    `json:"lib"`
	User      int    `json:"user"`
	Offset    int    `json:"off"`
	PairStart string `json:"start"`
}

type FileRecord = kvadapter.FileRecord

type FileReadResult int

const (
	TagFound       FileReadResult = iota // a recognised rating tag was extracted
	TagAbsent                            // file was read and parsed; no recognised tag
	FileUnreadable                       // I/O error, unsupported extension, or container parse failure
)

const (
	tagFound       = TagFound
	tagAbsent      = TagAbsent
	fileUnreadable = FileUnreadable
)

var defaultTagOrder = []string{"WMP", "iTunes", "MediaMonkey", "foobar2000", "MusicBee"}

const callBudget = 15 * time.Second

func parseCursor(payload string) (SyncCursor, bool) {
	if payload == "" {
		return SyncCursor{}, false
	}
	var c SyncCursor
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return SyncCursor{}, false
	}
	return c, true
}

func (c SyncCursor) marshal() string {
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}

func RunSyncStep(cfg PluginConfig, payload string) error {
	return runSyncStepUntil(cfg, payload, time.Now().Add(callBudget))
}

func RunSyncStepUntil(cfg PluginConfig, payload string, deadline time.Time) error {
	return runSyncStepUntil(cfg, payload, deadline)
}

func runSyncStep(cfg PluginConfig, payload string) error {
	return RunSyncStep(cfg, payload)
}

func runSyncStepUntil(cfg PluginConfig, payload string, deadline time.Time) error {
	if len(cfg.Libraries) == 0 {
		return fmt.Errorf("no libraries configured – add at least one library with users in the plugin settings")
	}
	kvadapter.EnsureConfigCacheIsCurrent(cfg)

	cur, resumed := parseCursor(payload)
	if !resumed && kvadapter.SweepInProgress() {
		return nil
	}
	kvadapter.MarkSweepActive()
	if resumed {
		// no-op, just resume
	} else {
		// no-op, just start
	}

	next, done := runSyncChunk(cfg, cur, deadline)
	if done {
		kvadapter.ClearSweepActive()
		return nil
	}
	if _, err := host.SchedulerScheduleOneTime(0, next.marshal(), ""); err != nil {
		return fmt.Errorf("failed to reschedule sync continuation: %w", err)
	}
	return nil
}

type ScanState = kvadapter.ScanState

func RunSyncChunk(cfg PluginConfig, cur SyncCursor, deadline time.Time) (SyncCursor, bool) {
	return runSyncChunk(cfg, cur, deadline)
}

func ProcessPairChunk(lib LibraryConfig, u UserConfig, cfg PluginConfig, cur SyncCursor, threshold time.Time, deadline time.Time, index map[string][]FileEntry, usePersistentIndex bool, bucketCache map[string][]FileRecord) (SyncCursor, bool) {
	return processPairChunk(lib, u, cfg, cur, threshold, deadline, index, usePersistentIndex, bucketCache)
}

func EnsureLibraryIndexed(libraryID string, deadline time.Time) (bool, error) {
	return ensureLibraryIndexed(libraryID, deadline)
}
