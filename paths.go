package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// File location for plugins
// ─────────────────────────
// Navidrome does NOT hand a plugin a usable filesystem path for a song: the
// Subsonic `search3` response carries either a synthesized "fake" path (built
// from tags) or, only when the player has Report Real Path enabled, the host's
// absolute path — neither of which is openable from inside the wasip1 sandbox.
//
// What a plugin CAN open is the library's mount point. With the manifest
// `library` permission and `filesystem: true`, Navidrome read-only mounts each
// assigned library at `/libraries/{id}` inside the sandbox and exposes that
// path via host.LibraryGetLibrary(...).MountPoint. We therefore walk the mount,
// index every audio file by its exact byte size, and match each Subsonic song
// to its file on size. Size is reliable because Navidrome stores the scanned
// file size and returns it in the `size` field.

// fileEntry is a real audio file discovered under a library mount point.
// Fields are exported so the entry can be JSON-marshaled into the KV-backed
// file-index cache (see loadCachedIndex / saveCachedIndex). Short JSON tags
// keep the serialised blob compact for libraries with thousands of files.
type fileEntry struct {
	Path  string    `json:"p"`
	Size  int64     `json:"s"`
	Mtime time.Time `json:"t"`
}

// resolveMountPoint maps a configured library ID to its in-sandbox mount point.
// The plugin config stores libraryId as a string, but the Library host service
// keys on the numeric ID, so we parse it here. An empty MountPoint means the
// host did not grant filesystem access (missing `library`+`filesystem:true`
// permission, or the library is not assigned to the plugin).
func resolveMountPoint(libraryID string) (string, error) {
	id, err := strconv.Atoi(strings.TrimSpace(libraryID))
	if err != nil {
		return "", fmt.Errorf("library ID %q is not numeric", libraryID)
	}
	lib, err := host.LibraryGetLibrary(int32(id))
	if err != nil {
		return "", err
	}
	if lib == nil || lib.MountPoint == "" {
		return "", errors.New("no filesystem mount point returned (grant the 'library' permission with filesystem access and assign this library to the plugin)")
	}
	return lib.MountPoint, nil
}

// isSupportedExt reports whether ext (lowercase, no dot) is a container the
// plugin can parse. Kept in sync with dispatchParser in scanner.go.
func isSupportedExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4":
		return true
	}
	return false
}

// sizeKey is the index/lookup key: exact byte size plus lowercase extension.
// Combining size with the extension keeps unrelated containers of a coincidental
// equal size in separate buckets.
func sizeKey(size int64, ext string) string {
	return strconv.FormatInt(size, 10) + ":" + ext
}

// buildFileIndex walks mountPoint recursively and indexes supported audio files
// by sizeKey. A bucket may hold more than one file when two files share an exact
// size and extension; matchFile treats that as ambiguous rather than guessing.
func buildFileIndex(mountPoint string) (map[string][]fileEntry, error) {
	index := map[string][]fileEntry{}
	err := walkAudioFiles(mountPoint, func(path string, size int64, mtime time.Time) {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
		k := sizeKey(size, ext)
		index[k] = append(index[k], fileEntry{Path: path, Size: size, Mtime: mtime})
	})
	if err != nil {
		return nil, err
	}
	return index, nil
}

// walkAudioFiles recurses root with os.ReadDir (the pattern proven by the
// artist-nfo plugin; avoids any TinyGo filepath.WalkDir edge cases) and invokes
// fn for every regular file with a supported extension. An unreadable
// sub-directory is logged and skipped so one bad folder cannot abort the scan;
// only a failure to read the root is returned as an error.
func walkAudioFiles(root string, fn func(path string, size int64, mtime time.Time)) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if e.IsDir() {
			if subErr := walkAudioFiles(full, fn); subErr != nil {
				logWarn(fmt.Sprintf("nd-rating-sync: cannot read directory %q (skipping)", full))
			}
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Name()), "."))
		if !isSupportedExt(ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fn(full, info.Size(), info.ModTime())
	}
	return nil
}

// matchFile finds the unique file for a song by (size, suffix). It deliberately
// refuses to guess: a size+suffix collision (more than one candidate) returns
// not-found, so an ambiguous match can never cause the wrong song to be rated.
// A missing match is handled by the caller as "unreadable" — never as
// "untagged" — so a file the plugin cannot locate is never cleared.
func matchFile(index map[string][]fileEntry, s subsonicSong) (fileEntry, bool) {
	cands := index[sizeKey(s.Size, strings.ToLower(s.Suffix))]
	if len(cands) != 1 {
		return fileEntry{}, false
	}
	return cands[0], true
}

// fileIndexResult is a memoised resolve+walk outcome: the file index for a
// library plus whether the mount could be resolved and read. A "false" entry
// means we already logged the failure and the caller should skip the pair
// without saving the threshold.
type fileIndexResult struct {
	index map[string][]fileEntry
	ok    bool
}

// cachedFileIndex returns the file index for libraryID, building it on first
// access and memoising the result for the lifetime of a single runSyncChunk
// call. State does not survive across callbacks, so this in-memory cache is
// created fresh per call. N users of one library therefore share one resolve.
//
// libCache is the same per-call libraryLastScan memoiser runSyncChunk uses for
// the gate, threaded through so the KV-backed cache (one layer down in
// resolveAndIndex) can validate against `LastScanAt` without a duplicate host
// call.
func cachedFileIndex(cache map[string]fileIndexResult, libCache map[string]libScanResult, libraryID string) (map[string][]fileEntry, bool) {
	if r, found := cache[libraryID]; found {
		return r.index, r.ok
	}
	idx, ok := resolveAndIndex(libCache, libraryID)
	cache[libraryID] = fileIndexResult{index: idx, ok: ok}
	return idx, ok
}

// resolveAndIndex returns the file index for libraryID, preferring a valid KV
// cache entry over a fresh mount walk.
//
// Walking the mount is the dominant cost of a chunk on slow filesystems
// (recursive `os.ReadDir` over thousands of files = many WASI host calls).
// Each callback is a fresh WASM instance, so without a KV cache we re-walk
// every chunk, which has been observed to eat 5–6 s of the 10 s budget on
// real libraries and reduce per-chunk song throughput to a trickle.
//
// Cache stamp: we use Navidrome's `LastScanAt` (the same value the gate uses)
// as the validity token. The cache survives as long as Navidrome hasn't
// rescanned the library; once it does, the next chunk's `LastScanAt` no
// longer matches and the cache is silently rebuilt and overwritten — no
// explicit invalidation needed, no "delete on sweep complete" book-keeping.
//
// Fail-open everywhere: any KV / parse / size-budget failure just falls
// through to a fresh walk, so the cache can only make things faster, never
// less correct.
func resolveAndIndex(libCache map[string]libScanResult, libraryID string) (map[string][]fileEntry, bool) {
	// Validity stamp: LastScanAt. Reused across this call thanks to libCache,
	// so caching adds zero extra host calls on the hot path.
	lastScan, hasScan := cachedLibraryLastScan(libCache, libraryID)

	if hasScan {
		if idx, ok := loadCachedIndex(libraryID, lastScan); ok {
			logDebug(fmt.Sprintf(
				"nd-rating-sync: file index cache hit for library=%q – %d size buckets",
				libraryID, len(idx)))
			return idx, true
		}
	}

	mountPoint, err := resolveMountPoint(libraryID)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot access filesystem for library=%q: %v – skipping pair", libraryID, err))
		return nil, false
	}
	idx, err := buildFileIndex(mountPoint)
	if err != nil {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: cannot read library mount %q – skipping pair", mountPoint))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: read mount %q error: %q", mountPoint, err.Error()))
		return nil, false
	}
	logDebug(fmt.Sprintf(
		"nd-rating-sync: indexed mount %q for library=%q – %d size buckets",
		mountPoint, libraryID, len(idx)))

	// Persist for the rest of this sweep's continuation chain. If LastScanAt
	// is unknown (0 / host error / non-numeric ID), skip persistence — we
	// cannot validate the cache on subsequent reads without it, so storing
	// would just leak stale data.
	if hasScan {
		saveCachedIndex(libraryID, lastScan, idx)
	}
	return idx, true
}

// ─── KV-backed file index cache ───────────────────────────────────────────────

// fileIndexCacheVersion bumps when the on-disk schema changes incompatibly so
// a stale entry from an older binary is treated as a miss rather than mis-parsed.
const fileIndexCacheVersion = 1

// maxIndexBytes caps the serialised size we are willing to persist. Above this
// the cache silently degrades to "rebuild every chunk" — better than blowing
// the KV with an oversized blob from an outlier library.
const maxIndexBytes = 4 * 1024 * 1024 // 4 MiB

// kvKeyFileIndex is the storage key for a library's cached file index.
// URL-escaping the libraryID prevents a future non-numeric ID containing a
// ':' from colliding with another key family.
func kvKeyFileIndex(libraryID string) string {
	return "file-index:" + url.QueryEscape(libraryID)
}

// cachedFileIndexBlob is the persisted form. Top-level fields are short so the
// per-entry overhead stays small for large libraries; on a 10k-file library
// the blob lands around 1 MiB.
type cachedFileIndexBlob struct {
	Version    int         `json:"v"`
	LastScanAt int64       `json:"l"` // Navidrome's LastScanAt (unix seconds) at build time
	Entries    []fileEntry `json:"e"`
}

// loadCachedIndex reads a cached file index from KV and validates it against
// the library's current LastScanAt. Any failure (KV miss, parse error, version
// or stamp mismatch) returns false so the caller rebuilds — fail-open.
func loadCachedIndex(libraryID string, expectedLastScan time.Time) (map[string][]fileEntry, bool) {
	key := kvKeyFileIndex(libraryID)
	raw, found, err := host.KVStoreGet(key)
	if err != nil {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: KVStoreGet(%q) failed: %q – rebuilding index", key, err.Error()))
		return nil, false
	}
	if !found || len(raw) == 0 {
		return nil, false
	}
	var blob cachedFileIndexBlob
	if err := json.Unmarshal(raw, &blob); err != nil {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: cached file index for %q is malformed (%q) – rebuilding", key, err.Error()))
		return nil, false
	}
	if blob.Version != fileIndexCacheVersion {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: cached file index for %q has version %d, want %d – rebuilding",
			key, blob.Version, fileIndexCacheVersion))
		return nil, false
	}
	if blob.LastScanAt != expectedLastScan.Unix() {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: cached file index for %q is stale (cached_scan=%d current_scan=%d) – rebuilding",
			key, blob.LastScanAt, expectedLastScan.Unix()))
		return nil, false
	}

	// Rebuild the bucket map from the flat entries slice. sizeKey is derived
	// from each entry's size+ext, so we do not store it in the blob.
	index := make(map[string][]fileEntry, len(blob.Entries))
	for _, e := range blob.Entries {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.Path), "."))
		k := sizeKey(e.Size, ext)
		index[k] = append(index[k], e)
	}
	return index, true
}

// saveCachedIndex persists the file index to KV. Errors and oversize blobs are
// non-fatal: at worst the next chunk does the same rebuild it would have done
// without a cache.
func saveCachedIndex(libraryID string, lastScan time.Time, index map[string][]fileEntry) {
	// Flatten the bucket map. Each fileEntry already carries everything needed
	// to reconstruct the bucket key (size+ext from Path) on load.
	var total int
	for _, bucket := range index {
		total += len(bucket)
	}
	entries := make([]fileEntry, 0, total)
	for _, bucket := range index {
		entries = append(entries, bucket...)
	}

	blob := cachedFileIndexBlob{
		Version:    fileIndexCacheVersion,
		LastScanAt: lastScan.Unix(),
		Entries:    entries,
	}
	raw, err := json.Marshal(blob)
	if err != nil {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: marshal cached file index for library=%q failed: %q", libraryID, err.Error()))
		return
	}
	if len(raw) > maxIndexBytes {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: cached file index for library=%q is %d bytes, exceeds cap %d – not persisting",
			libraryID, len(raw), maxIndexBytes))
		return
	}
	key := kvKeyFileIndex(libraryID)
	if err := host.KVStoreSet(key, raw); err != nil {
		logDebug(fmt.Sprintf(
			"nd-rating-sync: KVStoreSet(%q) failed: %q", key, err.Error()))
		return
	}
	logDebug(fmt.Sprintf(
		"nd-rating-sync: cached file index for library=%q – %d entries, %d bytes",
		libraryID, len(entries), len(raw)))
}
