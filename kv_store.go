package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

const kvKeyConfigHash = "config-hash"
const configPrefix = "config"
const bucketPrefix = "bucket"
const libraryStatePrefix = "libraryState"

// configHashFor returns a stable SHA256 hash of the resolved plugin config.
// It is used to detect config churn and trigger state resets when settings change.
func configHashFor(cfg pluginConfig) string {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ensureConfigCacheIsCurrent compares the current config hash with the last
// stored hash. When they differ, the plugin purges its own KV prefixes
// (bucket, libraryState, and config) so stale cached state cannot survive a
// config change. This is a plugin-namespace reset, not a targeted single-key
// delete.
func ensureConfigCacheIsCurrent() {
	current := configHashFor(loadConfig())
	if current == "" {
		return
	}

	raw, found, err := host.KVStoreGet(kvKeyConfigHash)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – config cache check could not run",
			kvKeyConfigHash, err.Error()))
		return
	}
	if found && len(raw) > 0 && string(raw) == current {
		logTrace(fmt.Sprintf(
			"nd-rating-sync: config state is current for %q",
			kvKeyConfigHash))
		return
	}

	deleted, err := purgeStaleKVEntries()
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: config change detected, but stale plugin KV purge failed for %q: %q — please clear the plugin-owned KV state manually",
			configPrefix, err.Error()))
	} else if deleted > 0 {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: config change detected; purged %d stale plugin KV entries",
			deleted))
	}
	if err := host.KVStoreSet(kvKeyConfigHash, []byte(current)); err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreSet(%q) failed: %q – new config hash could not be stored",
			kvKeyConfigHash, err.Error()))
		return
	}

	logTrace(fmt.Sprintf(
		"nd-rating-sync: config state updated for %q",
		kvKeyConfigHash))
}

// purgeStaleKVEntries deletes every plugin-owned KV entry under the prefixes
// that can contain stale derived state after a config change. Because the plugin
// runs in its own namespace, this is effectively a full reset of the plugin's
// cached state for those prefixes, not a single-key cleanup.
func purgeStaleKVEntries() (int64, error) {
	prefixes := []string{bucketPrefix, libraryStatePrefix, configPrefix}
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

func kvKeyLastSynced(libraryID, username string) string {
	return "last-synced:" + url.QueryEscape(libraryID) + ":" + url.QueryEscape(username)
}

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

func kvKeySweepActive() string {
	return "sweep-active"
}

const sweepStaleAfter = 2 * time.Minute

func sweepInProgress() bool {
	logTrace("nd-rating-sync: sweepInProgress start")
	raw, found, err := host.KVStoreGet(kvKeySweepActive())
	if err != nil {
		logTrace("nd-rating-sync: sweepInProgress stop, assume no sweep active")
		logWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – assuming no sweep active", kvKeySweepActive(), err.Error()))
		return false
	}
	if !found || len(raw) == 0 {
		logTrace("nd-rating-sync: sweepInProgress done, not found")
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, string(raw))
	if err != nil {
		logTrace("nd-rating-sync: sweepInProgress done, malformed heartbeat")
		return false
	}
	age := time.Since(t)
	logTrace("nd-rating-sync: sweepInProgress done")
	return age >= 0 && age < sweepStaleAfter
}

func markSweepActive() {
	value := []byte(time.Now().UTC().Format(time.RFC3339Nano))
	if err := host.KVStoreSet(kvKeySweepActive(), value); err != nil {
		logWarn(fmt.Sprintf("nd-rating-sync: KVStoreSet(%q) failed: %q", kvKeySweepActive(), err.Error()))
	}
}

func clearSweepActive() {
	if err := host.KVStoreDelete(kvKeySweepActive()); err != nil {
		logDebug(fmt.Sprintf("nd-rating-sync: KVStoreDelete(%q) failed: %q", kvKeySweepActive(), err.Error()))
	}
}

func bucketKey(libraryID string, size int64, ext string) string {
	return fmt.Sprintf("%s:%s:%d:%s", bucketPrefix, libraryID, size, strings.ToLower(ext))
}

func loadBucket(libraryID string, size int64, ext string) ([]FileRecord, error) {
	key := bucketKey(libraryID, size, ext)
	data, found, err := host.KVStoreGet(key)
	if err != nil {
		return nil, err
	}
	if !found || len(data) == 0 {
		return nil, nil
	}
	var records []FileRecord
	err = json.Unmarshal(data, &records)
	return records, err
}

func saveBucket(libraryID string, size int64, ext string, records []FileRecord) error {
	key := bucketKey(libraryID, size, ext)
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return host.KVStoreSet(key, data)
}

func libraryScanStateKey(libraryID string) string {
	return fmt.Sprintf("%s:%s", libraryStatePrefix, libraryID)
}

func loadLibraryScanState(libraryID string) (ScanState, error) {
	key := libraryScanStateKey(libraryID)
	data, found, err := host.KVStoreGet(key)
	if err != nil {
		return ScanState{}, err
	}
	if !found || len(data) == 0 {
		return ScanState{}, nil
	}
	var scanState ScanState
	err = json.Unmarshal(data, &scanState)
	return scanState, err
}

func saveLibraryScanState(libraryID string, state *ScanState) error {
	key := libraryScanStateKey(libraryID)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return host.KVStoreSet(key, data)
}

func getPercentageKVStorageUsage(kvStorageSize string) (float64, error) {
	bytes, err := humanize.ParseBytes(kvStorageSize)
	if err != nil {
		return 0, err
	}

	kvStorageUsed, err := host.KVStoreGetStorageUsed()
	if err != nil {
		return 0, err
	}
	if kvStorageUsed == 0 {
		return 0, nil
	}

	return float64(bytes) / float64(kvStorageUsed) * 100, nil
}
