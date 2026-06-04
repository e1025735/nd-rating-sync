package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// callBudget is how long a single scheduler callback is allowed to spend
// processing songs before it persists its progress and reschedules a
// continuation. Navidrome force-closes any plugin call that runs longer than
// 30s (extism manifest Timeout, hardcoded in the host – see
// plugins/manager.go defaultTimeout). We stop well short of that so the page
// fetch, one final (possibly 64 MiB) file read, and the reschedule call all
// have room to complete inside the host's hard limit.
//
// 10s was chosen after a production panic where a callback ran for ~16s and
// then crashed inside wasi clock_time_get: the host's wazero module was set
// up with WithCloseOnContextDone(true), and any cancellation in that window
// turns the next host call into a nil-pointer-dereference. A 10s budget keeps
// us comfortably under any plausible host timeout (15s, 20s, 30s) and gives
// generous slack for the trailing host calls.
const callBudget = 10 * time.Second

// deadlineCheckEvery is the number of songs processed between time.Now()
// deadline checks inside processPairChunk's hot loop. Each time.Now() is a
// clock_time_get host call; reducing the rate by a factor of 8 cuts the
// surface area of clock host calls 8x. With ~10ms per song typical, an 8-song
// burst is ~80ms of overshoot at worst — negligible against the 10s budget.
const deadlineCheckEvery = 8

// syncCursor is the resumable position of a sync. It is serialised into the
// scheduler callback payload so a long sync can be spread across many short
// callbacks: each call processes songs until callBudget elapses, then
// reschedules a one-time callback carrying the updated cursor.
//
// Package globals do NOT survive between callbacks – the host instantiates a
// fresh WASM module per call – so the payload (and the KV store) are the only
// channels through which progress persists. Indices reference the config as
// loaded at fire time; config is effectively stable across the seconds-apart
// continuations, and because setRating is idempotent a re-processed song is
// harmless if it ever shifts.
type syncCursor struct {
	Lib       int    `json:"lib"`   // index into cfg.Libraries
	User      int    `json:"user"`  // index into cfg.Libraries[Lib].Users
	Offset    int    `json:"off"`   // next song index to process within this (lib,user)
	PairStart string `json:"start"` // RFC3339Nano scan-start for the current pair; saved as the incremental threshold when the pair completes
}

// parseCursor decodes a scheduler payload into a syncCursor. An empty payload
// means a fresh full sweep starting at the first (lib,user). A malformed
// payload is logged and treated as a fresh sweep rather than aborting the sync
// – losing the cursor only costs a redundant (idempotent) pass.
//
// The bool reports whether a non-empty payload was successfully decoded; it is
// purely informational for callers/tests.
func parseCursor(payload string) (syncCursor, bool) {
	if payload == "" {
		return syncCursor{}, false
	}
	var c syncCursor
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: malformed continuation payload (%q) – restarting sync from the beginning", err.Error()))
		return syncCursor{}, false
	}
	return c, true
}

// marshal serialises the cursor for a continuation payload. Marshalling a
// handful of ints and a short string cannot realistically fail; on the
// impossible error we return "" so the continuation simply restarts the sweep.
func (c syncCursor) marshal() string {
	b, err := json.Marshal(c)
	if err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: failed to marshal continuation cursor: %q", err.Error()))
		return ""
	}
	return string(b)
}
