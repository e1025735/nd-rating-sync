package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// State is persisted in Navidrome's KVStore, which survives plugin reloads.
// Keys are plugin-scoped by the host so collisions with other plugins are
// impossible — we only need uniqueness within nd-rating-sync.

// kvKeyLastSynced is the storage key for the most recent successful scan
// timestamp of a single (library, user) tuple. Encoded as RFC3339Nano.
//
// Both components are URL-escaped so a libraryID or username that contains
// the ':' delimiter cannot collide with another tuple — without the escape,
// (libraryID="a:b", username="c") and (libraryID="a", username="b:c") would
// produce the same key. Typical UUID library IDs and alphanumeric usernames
// pass through unchanged.
func kvKeyLastSynced(libraryID, username string) string {
	return "last-synced:" + url.QueryEscape(libraryID) + ":" + url.QueryEscape(username)
}

// loadLastSynced returns the timestamp of the previous successful scan for
// the given (library, user) tuple, or the zero time if none is recorded.
//
// KV failures are not fatal: the function logs and returns zero time, which
// causes the caller to treat the upcoming scan as a full one. This keeps
// rating ingestion working even if the KV store is temporarily unavailable.
func loadLastSynced(libraryID, username string) time.Time {
	logTrace(fmt.Sprintf("nd-rating-sync: loadLastSynced start lib=%q user=%q", libraryID, username))
	key := kvKeyLastSynced(libraryID, username)
	raw, found, err := host.KVStoreGet(key)
	if err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: loadLastSynced stop, error lib=%q user=%q", libraryID, username))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – falling back to full scan", key, err.Error()))
		return time.Time{}
	}
	if !found || len(raw) == 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: loadLastSynced stop, not found lib=%q user=%q", libraryID, username))
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: loadLastSynced stop, time error lib=%q user=%q", libraryID, username))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: stored last-synced for %q is malformed (%q) – falling back to full scan", key, raw))
		return time.Time{}
	}
	logTrace(fmt.Sprintf("nd-rating-sync: loadLastSynced done lib=%q user=%q", libraryID, username))
	return t
}

// saveLastSynced records the scan-start timestamp so the next run can skip
// files whose mtime predates it. Errors are logged but not propagated — a
// failed write means the next run does redundant work, never incorrect work.
func saveLastSynced(libraryID, username string, t time.Time) {
	logTrace(fmt.Sprintf("nd-rating-sync: saveLastSynced start lib=%q user=%q, time=%q", libraryID, username, t))
	key := kvKeyLastSynced(libraryID, username)
	value := []byte(t.UTC().Format(time.RFC3339Nano))
	if err := host.KVStoreSet(key, value); err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: saveLastSynced stop, error lib=%q user=%q, time=%q", libraryID, username, t))
		logWarn(fmt.Sprintf("nd-rating-sync: KVStoreSet(%q) failed: %q", key, err.Error()))
	}
	logTrace(fmt.Sprintf("nd-rating-sync: saveLastSynced done lib=%q user=%q, time=%q", libraryID, username, t))
}

// ─── In-progress guard ──────────────────────────────────────────────────────

// kvKeySweepActive marks that a full sweep's continuation chain is currently
// running, so a freshly-triggered sweep (cron / immediate) can skip rather than
// run concurrently and duplicate work. The value is an RFC3339Nano heartbeat
// refreshed on every continuation; a sweep counts as active only while that
// heartbeat is younger than sweepStaleAfter, so a crashed chain self-heals on
// the next run instead of blocking sweeps forever.
const kvKeySweepActive = "sweep-active"

// sweepStaleAfter is how long after the last heartbeat a sweep is still
// considered in progress. It must comfortably exceed one chunk cycle (callBudget
// plus a final file read and the reschedule, all under the host's 30s limit) so
// a live-but-slow chain is never mistaken for a crashed one.
const sweepStaleAfter = 2 * time.Minute

// sweepInProgress reports whether another full sweep's continuation chain is
// running (heartbeat present and fresh). KV failures are non-fatal: on error or
// a malformed/stale value it returns false (fail open) so a sync is never
// blocked by KV trouble — at worst two sweeps overlap, which setRating
// idempotency tolerates.
func sweepInProgress() bool {
	logTrace("nd-rating-sync: sweepInProgress start")
	raw, found, err := host.KVStoreGet(kvKeySweepActive)
	if err != nil {
		logTrace("nd-rating-sync: sweepInProgress stop, assume no sweep active")
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – assuming no sweep active", kvKeySweepActive, err.Error()))
		return false
	}
	if !found || len(raw) == 0 {
		logTrace("nd-rating-sync: sweepInProgress done, not found")
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		// Malformed heartbeat: treat as stale so a fresh sweep can overwrite it.
		logTrace("nd-rating-sync: sweepInProgress done, malformed heartbeat")
		return false
	}
	// A future-dated heartbeat (age < 0) means the system clock stepped backward
	// since it was written (NTP correction, snapshot restore, manual change).
	// Treat it as stale too — fail open like every other uncertain case here,
	// rather than suppressing sweeps until real time catches up.
	age := time.Since(t)
	logTrace("nd-rating-sync: sweepInProgress done")
	return age >= 0 && age < sweepStaleAfter
}

// markSweepActive writes/refreshes the in-progress heartbeat. Errors are logged
// but not propagated — a failed write only weakens overlap protection.
func markSweepActive() {
	value := []byte(time.Now().UTC().Format(time.RFC3339Nano))
	if err := host.KVStoreSet(kvKeySweepActive, value); err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: KVStoreSet(%q) failed: %q", kvKeySweepActive, err.Error()))
	}
}

// clearSweepActive removes the in-progress heartbeat once a sweep completes (and
// on plugin init, since a reload kills any running chain). A failed delete only
// means the next fresh sweep waits out sweepStaleAfter before proceeding.
func clearSweepActive() {
	if err := host.KVStoreDelete(kvKeySweepActive); err != nil {
		logDebug(fmt.Sprintf("nd-rating-sync: KVStoreDelete(%q) failed: %q", kvKeySweepActive, err.Error()))
	}
}
