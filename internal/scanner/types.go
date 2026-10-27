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
	IncrementalSync              bool
	DryRun                       bool
	DefaultSkipAlreadyRated      *bool
	DefaultClearRatingIfUntagged *bool
	Libraries                    []LibraryConfig
}

type FileEntry struct {
	Path  string
	Size  int64
	MTime time.Time
}

// SyncCursor is the resumable position of a sync. It is serialised into the
// scheduler callback payload so a long sync can be spread across many short
// callbacks: each call processes songs until callBudget elapses, then
// reschedules a one-time callback carrying the updated cursor.
//
// Package globals do NOT survive between callbacks – the host instantiates a
// fresh WASM module per call – so the payload (and the KV store) are the only
// channels through which progress persists. Indices reference the config as
// loaded at fire time; because setRating is idempotent a re-processed song is
// harmless if they ever shift.
type SyncCursor struct {
	Lib       int    `json:"lib"`   // index into cfg.Libraries
	User      int    `json:"user"`  // index into cfg.Libraries[Lib].Users
	Offset    int    `json:"off"`   // next song index to process within this (lib,user)
	PairStart string `json:"start"` // RFC3339Nano scan-start for the current pair; saved as the incremental threshold when the pair completes
	// Retry counts attempts at the current pair that failed without
	// progress (see maxPairAttempts). Non-zero delays the continuation.
	Retry int `json:"retry,omitempty"`
}

// nextPair moves the cursor to the next user of the current library and
// clears everything that belongs to the pair it leaves.
func (c *SyncCursor) nextPair() {
	c.User++
	c.Offset = 0
	c.PairStart = ""
	c.Retry = 0
}

type FileReadResult int

const (
	TagFound       FileReadResult = iota // a recognised rating tag was extracted
	TagAbsent                            // file was read and parsed; no recognised tag
	FileUnreadable                       // I/O error, unsupported extension, or corrupt tag
)

const (
	tagFound       = TagFound
	tagAbsent      = TagAbsent
	fileUnreadable = FileUnreadable
)

// DefaultTagOrder is the ratingTagOrder of a user who does not set one. A
// change to it changes those users' pair fingerprint, so their next sweep
// reads every file once.
var DefaultTagOrder = []string{"WMP", "iTunes", "MediaMonkey", "foobar2000", "MusicBee", "FMPS"}

// callBudget is how long one callback works before it hands over to a
// continuation. Navidrome force-closes a call after 30 s; the budget stays
// well below that (see CLAUDE.md, "Sync execution").
const callBudget = 15 * time.Second

const (
	// maxPairAttempts bounds how often one pair is retried after a callback
	// that made no progress on it: the song listing or the matcher lookup of
	// a page failed. Without it a lasting failure (a username Navidrome does
	// not know) would re-run back to back forever. After the last attempt
	// the pair is skipped until the next sweep, without saving its threshold.
	maxPairAttempts = 3
	// retryDelaySeconds spaces those retries out. It must stay well below
	// kvadapter.SweepStaleAfter so the waiting chain still counts as in
	// progress and a cron firing does not start a second one.
	retryDelaySeconds = 30
)

// parseCursor decodes a scheduler payload into a SyncCursor. An empty payload
// means a fresh full sweep starting at the first (lib,user). A malformed
// payload is logged and treated as a fresh sweep rather than aborting the sync
// – losing the cursor only costs a redundant (idempotent) pass.
//
// The bool reports whether a non-empty payload was successfully decoded, i.e.
// whether this callback continues a running sweep.
func parseCursor(payload string) (SyncCursor, bool) {
	if payload == "" {
		return SyncCursor{}, false
	}
	var c SyncCursor
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: malformed continuation payload (%q) – restarting sync from the beginning", err.Error()))
		return SyncCursor{}, false
	}
	return c, true
}

// marshal serialises the cursor for a continuation payload. Marshalling a
// handful of ints and a short string cannot realistically fail; on the
// impossible error we return "" so the continuation simply restarts the sweep.
func (c SyncCursor) marshal() string {
	b, err := json.Marshal(c)
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: failed to marshal continuation cursor: %q", err.Error()))
		return ""
	}
	return string(b)
}

// RunSyncStep runs one callBudget slice of a sync: it resumes the sweep the
// payload describes (or starts a fresh one) and schedules a continuation when
// the budget runs out first.
//
// There is deliberately no recover() here: TinyGo cannot recover panics on
// WebAssembly (the module exits without running deferred functions), so the
// code below must be panic-free rather than rely on a guard that only works
// under `go test`.
func RunSyncStep(cfg PluginConfig, payload string) error {
	return runSyncStepUntil(cfg, payload, time.Now().Add(callBudget))
}

func runSyncStepUntil(cfg PluginConfig, payload string, deadline time.Time) error {
	if len(cfg.Libraries) == 0 {
		return fmt.Errorf("no libraries configured – add at least one library with users in the plugin settings")
	}
	cur, resumed := parseCursor(payload)
	if !resumed && kvadapter.SweepInProgress() {
		logInfo("nd-rating-sync: a sweep is already in progress – skipping this run")
		return nil
	}
	kvadapter.MarkSweepActive()

	next, done := runSyncChunk(cfg, cur, deadline)
	if done {
		kvadapter.ClearSweepActive()
		logInfo("nd-rating-sync: sync complete")
		return nil
	}
	delay := int32(0)
	if next.Retry > 0 {
		delay = retryDelaySeconds
	}
	if _, err := host.SchedulerScheduleOneTime(delay, next.marshal(), ""); err != nil {
		return fmt.Errorf("failed to reschedule sync continuation: %w", err)
	}
	logInfo(fmt.Sprintf(
		"nd-rating-sync: time budget reached – rescheduled continuation in %ds (lib=%d user=%d offset=%d)",
		delay, next.Lib, next.User, next.Offset))
	return nil
}

// pairFingerprint identifies the settings of one (library, user) pair that
// decide what a sweep writes. It is stored with the pair's incremental
// threshold, so changing any of them makes the next sweep read every file
// once instead of skipping unchanged ones.
func pairFingerprint(u UserConfig) string {
	raw, err := json.Marshal(struct {
		Order []string `json:"order"`
		Skip  bool     `json:"skip"`
		Clear bool     `json:"clear"`
	}{u.RatingTagOrder, u.SkipAlreadyRated, u.ClearRatingIfUntagged})
	if err != nil {
		return ""
	}
	return kvadapter.ConfigHashFor(raw)[:16]
}
