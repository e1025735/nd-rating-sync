package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const LibraryUserLastSyncPrefix = "last-synced"

// Key prefixes of the persistent file index that versions before the switch
// to the Navidrome matcher kept in the KV store (index buckets, build state
// and the config hash that guarded them). Nothing writes them any more;
// PurgeLegacyFileIndex removes what an older version left behind.
const (
	legacyBucketPrefix       = "bucket"
	legacyLibraryStatePrefix = "libraryState"
	legacyConfigPrefix       = "config"
)

const SweepStaleAfter = 2 * time.Minute

// ConfigHashFor returns a stable SHA256 hash for a given config payload.
func ConfigHashFor(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// PurgeLegacyFileIndex deletes the persistent file index an older version of
// the plugin may have stored, so its space is freed. It is cheap when there
// is nothing to delete and is run on every plugin load.
func PurgeLegacyFileIndex() (int64, error) {
	prefixes := []string{legacyBucketPrefix, legacyLibraryStatePrefix, legacyConfigPrefix}
	var deleted int64
	for _, prefix := range prefixes {
		count, err := host.KVStoreDeleteByPrefix(prefix)
		if err != nil {
			return deleted, err
		}
		deleted += count
	}
	return deleted, nil
}

func KVKeyLastSynced(libraryID, username string) string {
	return LibraryUserLastSyncPrefix + ":" + url.QueryEscape(libraryID) + ":" + url.QueryEscape(username)
}

// LoadLastSynced returns the incremental threshold of a (library, user) pair,
// or the zero time (full scan) when none is stored, it is unreadable, or it
// was saved with different pair settings: fingerprint describes the settings
// that decide what a sweep writes (tag order, skip/clear flags), so changing
// any of them re-reads every file once. A value without a fingerprint (saved
// by an older version) is accepted as is.
func LoadLastSynced(libraryID, username, fingerprint string) time.Time {
	key := KVKeyLastSynced(libraryID, username)
	raw, found, err := host.KVStoreGet(key)
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – falling back to full scan", key, err.Error()))
		return time.Time{}
	}
	if !found || len(raw) == 0 {
		return time.Time{}
	}
	stamp, stored, hasFingerprint := strings.Cut(string(raw), " ")
	kvLogTrace(fmt.Sprintf("nd-rating-sync: LoadLastSynced key=%q stamp=%q stored_fingerprint=%q want_fingerprint=%q", key, stamp, stored, fingerprint))
	if hasFingerprint && stored != fingerprint {
		kvLogInfo(fmt.Sprintf(
			"nd-rating-sync: settings for %q changed since the last sync – running a full scan", key))
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: stored last-synced for %q is malformed (%q) – falling back to full scan", key, raw))
		return time.Time{}
	}
	return t
}

// DeleteStaleLastSynced removes the incremental thresholds of (library, user)
// pairs that are no longer configured; keep holds the keys of the configured
// pairs (KVKeyLastSynced). It returns how many keys were deleted. A failed
// listing or delete only leaves a few small keys behind, so the caller logs
// it and carries on.
func DeleteStaleLastSynced(keep map[string]bool) (int, error) {
	keys, err := host.KVStoreList(LibraryUserLastSyncPrefix + ":")
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, key := range keys {
		if keep[key] {
			continue
		}
		if err := host.KVStoreDelete(key); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// SaveLastSynced stores t together with the pair's settings fingerprint
// ("<RFC3339Nano> <fingerprint>"; the timestamp never contains a space).
func SaveLastSynced(libraryID, username string, t time.Time, fingerprint string) {
	key := KVKeyLastSynced(libraryID, username)
	value := []byte(t.UTC().Format(time.RFC3339Nano) + " " + fingerprint)
	if err := host.KVStoreSet(key, value); err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreSet(%q) failed: %q – the next sweep of this pair reads every file again", key, err.Error()))
	}
}

func KVKeySweepActive() string {
	return "sweep-active"
}

// SweepRecord is the value of the sweep-active key: the heartbeat of a
// running sweep plus where it was last known to be.
//
// The record is refreshed at the start of every callback and of every song
// page, and deleted when a sweep completes. A record that is still there
// after SweepStaleAfter therefore means the chain stopped mid-sweep — a
// callback that crashed (panic, out of memory, the host's 30 s limit) or a
// reload. Cursor lets the next sweep resume there; Interrupted marks a stop
// that was not a crash (a reload, a continuation the host refused), after
// which the sweep resumes without hunting for a file that crashed it.
type SweepRecord struct {
	At          time.Time       `json:"at"`
	Cursor      json.RawMessage `json:"cursor,omitempty"`
	Interrupted bool            `json:"interrupted,omitempty"`
}

// Active reports whether a sweep is still running: a heartbeat younger than
// SweepStaleAfter that was not interrupted. A future-dated heartbeat (the
// clock stepped back) counts as stale.
func (r SweepRecord) Active() bool {
	age := time.Since(r.At)
	return !r.Interrupted && age >= 0 && age < SweepStaleAfter
}

// LoadSweepRecord returns the stored sweep record. Any failure — a KV error,
// no record, an unreadable one — reports false, so a sync is never blocked by
// it. A plain timestamp written by older versions is a record without cursor.
func LoadSweepRecord() (SweepRecord, bool) {
	raw, found, err := host.KVStoreGet(KVKeySweepActive())
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – assuming no sweep active", KVKeySweepActive(), err.Error()))
		return SweepRecord{}, false
	}
	if !found || len(raw) == 0 {
		return SweepRecord{}, false
	}
	var r SweepRecord
	if raw[0] == '{' {
		if err := json.Unmarshal(raw, &r); err != nil {
			return SweepRecord{}, false
		}
		return r, true
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return SweepRecord{}, false
	}
	return SweepRecord{At: t}, true
}

// MarkSweepActive refreshes the heartbeat and records cursor (a marshalled
// scanner cursor) as the position the sweep has reached.
func MarkSweepActive(cursor string) {
	saveSweepRecord(SweepRecord{At: time.Now().UTC(), Cursor: rawCursor(cursor)})
}

// MarkSweepInterrupted records that the running sweep stopped without a crash,
// so the next sweep resumes at its position without the crash hunt. An empty
// cursor keeps the stored position; nothing is written when no sweep is
// recorded.
func MarkSweepInterrupted(cursor string) {
	r, found := LoadSweepRecord()
	if !found && cursor == "" {
		return
	}
	if cursor != "" {
		r.Cursor = rawCursor(cursor)
	}
	r.Interrupted = true
	saveSweepRecord(r)
}

func rawCursor(cursor string) json.RawMessage {
	if cursor == "" || !json.Valid([]byte(cursor)) {
		return nil
	}
	return json.RawMessage(cursor)
}

func saveSweepRecord(r SweepRecord) {
	value, err := json.Marshal(r)
	if err == nil {
		err = host.KVStoreSet(KVKeySweepActive(), value)
	}
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreSet(%q) failed: %q – overlapping sweeps are not prevented and a crashed sweep restarts from the beginning until it works again", KVKeySweepActive(), err.Error()))
	}
}

func ClearSweepActive() {
	if err := host.KVStoreDelete(KVKeySweepActive()); err != nil {
		kvLogDebug(fmt.Sprintf("nd-rating-sync: KVStoreDelete(%q) failed: %q", KVKeySweepActive(), err.Error()))
	}
}
