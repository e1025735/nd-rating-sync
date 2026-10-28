package scanner

import (
	"encoding/json"
	"fmt"
	"time"

	kvadapter "github.com/e1025735/nd-rating-sync/internal/adapter/kv_store"
	"github.com/e1025735/nd-rating-sync/internal/logging"
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

// FileEntry is a song's file inside the library mount.
type FileEntry struct {
	Path  string
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
	// LogReadsUntil is set after a crash: up to this song offset (the end of
	// the page the crash happened on) every file is logged as a warning
	// before it is read, so a file that crashes the plugin again is the last
	// one in the log. 0 = normal logging.
	LogReadsUntil int `json:"logto,omitempty"`
}

// nextPair moves the cursor to the next user of the current library and
// clears everything that belongs to the pair it leaves.
func (c *SyncCursor) nextPair() {
	*c = SyncCursor{Lib: c.Lib, User: c.User + 1}
}

// nextLibrary moves the cursor to the first user of the next library.
func (c *SyncCursor) nextLibrary() {
	*c = SyncCursor{Lib: c.Lib + 1}
}

// pageEnd is the offset right after the song page that holds offset.
func pageEnd(offset int) int {
	return (offset/songPageSize + 1) * songPageSize
}

type FileReadResult int

const (
	TagFound        FileReadResult = iota // a recognised rating tag was extracted
	TagAbsent                             // file was read and parsed; no rating: it is rated 0 stars
	FileUnreadable                        // I/O error, unsupported extension, or corrupt tag
	TagNotWholeStar                       // rated, but with a half star or an out-of-range value
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
		logging.Warn(fmt.Sprintf(
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
		logging.Warn(fmt.Sprintf("nd-rating-sync: failed to marshal continuation cursor: %q", err.Error()))
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
	if !resumed {
		var busy bool
		cur, busy = startOrResumeSweep(cfg)
		if busy {
			logging.Info("nd-rating-sync: a sweep is already in progress – skipping this run")
			return nil
		}
	}
	kvadapter.MarkSweepActive(cur.marshal())

	next, done := runSyncChunk(cfg, cur, deadline)
	if done {
		kvadapter.ClearSweepActive()
		deleteStaleThresholds(cfg)
		logging.Info("nd-rating-sync: sync complete")
		return nil
	}
	delay := int32(0)
	if next.Retry > 0 {
		delay = retryDelaySeconds
	}
	if _, err := host.SchedulerScheduleOneTime(delay, next.marshal(), ""); err != nil {
		// Not a crash: the next sweep resumes here without the crash hunt.
		kvadapter.MarkSweepInterrupted(next.marshal())
		return fmt.Errorf("failed to reschedule sync continuation: %w", err)
	}
	logging.Info(fmt.Sprintf(
		"nd-rating-sync: time budget reached – rescheduled continuation in %ds (lib=%d user=%d offset=%d)",
		delay, next.Lib, next.User, next.Offset))
	return nil
}

// deleteStaleThresholds removes the incremental thresholds of (library,
// user) pairs that were removed from the config, once a sweep completes.
func deleteStaleThresholds(cfg PluginConfig) {
	keep := map[string]bool{}
	for _, lib := range cfg.Libraries {
		for _, u := range lib.Users {
			keep[kvadapter.KVKeyLastSynced(lib.LibraryID, u.Username)] = true
		}
	}
	deleted, err := kvadapter.DeleteStaleLastSynced(keep)
	switch {
	case err != nil:
		logging.Warn(fmt.Sprintf("nd-rating-sync: removing the sync state of users and libraries no longer configured failed: %q", err.Error()))
	case deleted > 0:
		logging.Info(fmt.Sprintf("nd-rating-sync: removed the sync state of %d user/library pairs no longer configured", deleted))
	}
}

// startOrResumeSweep decides where a callback without a continuation payload
// (the cron or the run after a load) starts. busy reports that another sweep
// is still running.
//
// The sweep record (see kvadapter.SweepRecord) is deleted when a sweep
// completes, so a record that is still there means the last sweep stopped
// mid-way, and the sweep resumes at its cursor instead of starting over:
//   - after a reload or a refused continuation (Interrupted), as if nothing
//     happened;
//   - otherwise the callback crashed: the plugin cannot log anything once it
//     is dead, so the sweep resumes at the start of the page it was on and
//     logs every file read on that page as a warning before reading it. If a
//     file crashes the plugin again, it is the last file in the log; the
//     next page is read normally again.
func startOrResumeSweep(cfg PluginConfig) (cur SyncCursor, busy bool) {
	rec, found := kvadapter.LoadSweepRecord()
	if !found {
		return SyncCursor{}, false
	}
	if rec.Active() {
		return SyncCursor{}, true
	}
	if len(rec.Cursor) == 0 {
		return SyncCursor{}, false
	}
	cur, ok := parseCursor(string(rec.Cursor))
	if !ok {
		return SyncCursor{}, false
	}
	cur.LogReadsUntil = 0
	where := describeCursor(cfg, cur)
	if rec.Interrupted {
		logging.Info(fmt.Sprintf("nd-rating-sync: resuming the interrupted sync at %s", where))
		return cur, false
	}
	cur.LogReadsUntil = pageEnd(cur.Offset)
	logging.Warn(fmt.Sprintf(
		"nd-rating-sync: the previous sync stopped unexpectedly at %s (the plugin crashed or exceeded Navidrome's 30 s limit) – resuming there and logging every file read up to song offset %d; if the plugin stops again, the last \"reading\" warning before the stop names the file that causes it",
		where, cur.LogReadsUntil))
	return cur, false
}

// describeCursor names the position of cur for a log line.
func describeCursor(cfg PluginConfig, cur SyncCursor) string {
	if cur.Lib < len(cfg.Libraries) && cur.User < len(cfg.Libraries[cur.Lib].Users) {
		lib := cfg.Libraries[cur.Lib]
		return fmt.Sprintf("user=%q library=%q song offset %d", lib.Users[cur.User].Username, lib.LibraryID, cur.Offset)
	}
	return fmt.Sprintf("library #%d user #%d song offset %d", cur.Lib, cur.User, cur.Offset)
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
