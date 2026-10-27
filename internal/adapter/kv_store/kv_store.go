package adapter

import (
	"crypto/sha256"
	"encoding/hex"
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

func SweepInProgress() bool {
	raw, found, err := host.KVStoreGet(KVKeySweepActive())
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – assuming no sweep active", KVKeySweepActive(), err.Error()))
		return false
	}
	if !found || len(raw) == 0 {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		return false
	}
	age := time.Since(t)
	return age >= 0 && age < SweepStaleAfter
}

func MarkSweepActive() {
	value := []byte(time.Now().UTC().Format(time.RFC3339Nano))
	if err := host.KVStoreSet(KVKeySweepActive(), value); err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreSet(%q) failed: %q – overlapping sweeps are not prevented until it works again", KVKeySweepActive(), err.Error()))
	}
}

func ClearSweepActive() {
	if err := host.KVStoreDelete(KVKeySweepActive()); err != nil {
		kvLogDebug(fmt.Sprintf("nd-rating-sync: KVStoreDelete(%q) failed: %q", KVKeySweepActive(), err.Error()))
	}
}
