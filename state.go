package main

import (
	"fmt"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// State is persisted in Navidrome's KVStore, which survives plugin reloads.
// Keys are plugin-scoped by the host so collisions with other plugins are
// impossible — we only need uniqueness within nd-rating-sync.

// kvKeyLastSynced is the storage key for the most recent successful scan
// timestamp of a single (library, user) tuple. Encoded as RFC3339Nano.
func kvKeyLastSynced(libraryID, username string) string {
	return "last-synced:" + libraryID + ":" + username
}

// loadLastSynced returns the timestamp of the previous successful scan for
// the given (library, user) tuple, or the zero time if none is recorded.
//
// KV failures are not fatal: the function logs and returns zero time, which
// causes the caller to treat the upcoming scan as a full one. This keeps
// rating ingestion working even if the KV store is temporarily unavailable.
func loadLastSynced(libraryID, username string) time.Time {
	key := kvKeyLastSynced(libraryID, username)
	raw, found, err := host.KVStoreGet(key)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %v – falling back to full scan", key, err))
		return time.Time{}
	}
	if !found || len(raw) == 0 {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: stored last-synced for %q is malformed (%q) – falling back to full scan", key, raw))
		return time.Time{}
	}
	return t
}

// saveLastSynced records the scan-start timestamp so the next run can skip
// files whose mtime predates it. Errors are logged but not propagated — a
// failed write means the next run does redundant work, never incorrect work.
func saveLastSynced(libraryID, username string, t time.Time) {
	key := kvKeyLastSynced(libraryID, username)
	value := []byte(t.UTC().Format(time.RFC3339Nano))
	if err := host.KVStoreSet(key, value); err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: KVStoreSet(%q) failed: %v", key, err))
	}
}
