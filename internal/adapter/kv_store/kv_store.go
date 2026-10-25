package adapter

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

const (
	KVKeyConfigHash           = "config-hash"
	ConfigPrefix              = "config"
	BucketPrefix              = "bucket"
	LibraryStatePrefix        = "libraryState"
	LibraryUserLastSyncPrefix = "last-synced"
)

const SweepStaleAfter = 2 * time.Minute

// ScanState is the progress of a persistent file-index build for one library.
// PendingDirs is a FIFO queue; the head directory is the one being read, and
// ResumeAfter is the name of the last entry of it already indexed (ReadDir
// returns entries sorted by name, so a build cut off by the deadline resumes
// right after it). A directory is popped only once all its entries are done.
type ScanState struct {
	Complete    bool
	PendingDirs []string
	ResumeAfter string `json:",omitempty"`
	LastScanAt  int64
}

// FileRecord is one file in a persistent index bucket. Mtime is in whole
// seconds; see the scanner's matchFileInBucketCache for how it is compared.
type FileRecord struct {
	Path  string
	Mtime int64
}

// ConfigHashFor returns a stable SHA256 hash for a given config payload.
func ConfigHashFor(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// EnsureConfigCacheIsCurrent compares the hash of cfg — only the settings
// the persistent file index depends on, see scanner.FileCacheConfig — with
// the stored one and purges the file-index state when it changed. Incremental
// thresholds are not purged here: each one carries a fingerprint of its own
// pair's settings (see SaveLastSynced).
func EnsureConfigCacheIsCurrent(cfg any) {
	currentHash := ConfigHashFor(mustJSONBytes(cfg))
	if currentHash == "" {
		return
	}

	raw, found, err := host.KVStoreGet(KVKeyConfigHash)
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – config cache check could not run",
			KVKeyConfigHash, err.Error()))
		return
	}
	if found && len(raw) > 0 && string(raw) == currentHash {
		return
	}

	kvLogTrace(fmt.Sprintf("nd-rating-sync: file-index config hash changed stored=%q current=%q", raw, currentHash))
	deleted, err := PurgeStaleKVEntries()
	if err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: config change detected, but stale plugin KV purge failed for %q: %q — please clear the plugin-owned KV state manually",
			ConfigPrefix, err.Error()))
	} else if deleted > 0 {
		kvLogInfo(fmt.Sprintf(
			"nd-rating-sync: config change detected; purged %d stale plugin KV entries",
			deleted))
	}
	if err := host.KVStoreSet(KVKeyConfigHash, []byte(currentHash)); err != nil {
		kvLogWarn(fmt.Sprintf(
			"nd-rating-sync: KVStoreSet(%q) failed: %q – new config hash could not be stored",
			KVKeyConfigHash, err.Error()))
		return
	}
}

func mustJSONBytes(cfg any) []byte {
	if cfg == nil {
		return nil
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil
	}
	return raw
}

func PurgeStaleKVEntries() (int64, error) {
	prefixes := []string{BucketPrefix, LibraryStatePrefix, ConfigPrefix}
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
		kvLogWarn(fmt.Sprintf("nd-rating-sync: KVStoreSet(%q) failed: %q", key, err.Error()))
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
		kvLogWarn(fmt.Sprintf("nd-rating-sync: KVStoreSet(%q) failed: %q", KVKeySweepActive(), err.Error()))
	}
}

func ClearSweepActive() {
	if err := host.KVStoreDelete(KVKeySweepActive()); err != nil {
		kvLogDebug(fmt.Sprintf("nd-rating-sync: KVStoreDelete(%q) failed: %q", KVKeySweepActive(), err.Error()))
	}
}

func BucketKey(libraryID string, size int64, ext string) string {
	return fmt.Sprintf("%s:%s:%d:%s", BucketPrefix, libraryID, size, strings.ToLower(ext))
}

// DeleteLibraryBuckets removes every index bucket of one library. A new index
// build starts with it, so records of moved or deleted files cannot survive
// into the new index and make a song's match ambiguous.
func DeleteLibraryBuckets(libraryID string) (int64, error) {
	return host.KVStoreDeleteByPrefix(BucketPrefix + ":" + libraryID + ":")
}

func LoadBucket(libraryID string, size int64, ext string) ([]FileRecord, error) {
	key := BucketKey(libraryID, size, ext)
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

func SaveBucket(libraryID string, size int64, ext string, records []FileRecord) error {
	key := BucketKey(libraryID, size, ext)
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	return host.KVStoreSet(key, data)
}

func LibraryScanStateKey(libraryID string) string {
	return fmt.Sprintf("%s:%s", LibraryStatePrefix, libraryID)
}

func LoadLibraryScanState(libraryID string) (ScanState, error) {
	key := LibraryScanStateKey(libraryID)
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

func SaveLibraryScanState(libraryID string, state *ScanState) error {
	key := LibraryScanStateKey(libraryID)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return host.KVStoreSet(key, data)
}

func GetPercentageKVStorageUsage(kvStorageSize string) (float64, error) {
	bytes, err := humanize.ParseBytes(kvStorageSize)
	if err != nil {
		return 0, err
	}

	if bytes == 0 {
		return 0, fmt.Errorf("KV storage size %q is zero", kvStorageSize)
	}
	kvStorageUsed, err := host.KVStoreGetStorageUsed()
	if err != nil {
		return 0, err
	}

	return float64(kvStorageUsed) / float64(bytes) * 100, nil
}
