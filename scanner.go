package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ─── Sync ─────────────────────────────────────────────────────────────────────

// runSyncChunk processes as many songs as fit before deadline, starting from
// cur, and returns the position to resume at plus whether the whole sweep is
// finished. It walks (library, user) pairs in order: for each fresh pair it
// stamps PairStart (the eventual incremental threshold) and, once the pair is
// fully processed, persists that threshold. When the deadline is reached
// mid-sweep it returns allDone=false with a cursor the caller hands to a
// continuation callback.
//
// Forward progress is guaranteed: the budget is only checked at pair
// boundaries here and after each song in processPairChunk, so every invocation
// either advances the cursor or completes the sweep – a chain of continuations
// always terminates.
func runSyncChunk(cfg pluginConfig, cur syncCursor, deadline time.Time) (syncCursor, bool) {
	// Per-call caches: LibraryGetLibrary results (for the LastScanAt gate) and
	// file-index results (for size-based file matching) so multiple users of
	// one library share a single host call and a single mount walk. Globals
	// do not persist across callbacks, so both are intentionally scoped to
	// one invocation.
	libCache := map[string]libScanResult{}
	indexCache := map[string]fileIndexResult{}

	logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk start lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
	for {
		// Skip exhausted users/libraries. Also tolerates indices that point
		// past the end after a config change between continuations.
		for cur.Lib < len(cfg.Libraries) && cur.User >= len(cfg.Libraries[cur.Lib].Users) {
			cur.Lib++
			cur.User = 0
			cur.Offset = 0
			cur.PairStart = ""
		}
		if cur.Lib >= len(cfg.Libraries) {
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, sweep complete lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			return cur, true // whole sweep complete
		}
		// A valid pair is selected. If the budget is gone, resume here.
		if time.Now().After(deadline) {
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline reached lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			return cur, false
		}

		lib := cfg.Libraries[cur.Lib]
		u := lib.Users[cur.User]
		if u.Username == "" {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: skipping library=%q user#%d – empty username in config", lib.LibraryID, cur.User))
			cur.User++
			cur.Offset = 0
			cur.PairStart = ""
			continue
		}

		// Load the incremental threshold once for this pair; reused by the
		// LastScanAt gate below and passed into processPairChunk for the
		// per-file mtime skip.
		var threshold time.Time
		if cfg.IncrementalSync {
			threshold = loadLastSynced(lib.LibraryID, u.Username)
		}

		// LastScanAt gate: when starting a fresh pair, skip the whole pair if
		// Navidrome has not rescanned the library since our last completed sweep
		// — no song paging at all. A gated skip must NOT save the threshold:
		// nothing was processed, so it stays pinned to the last real sweep and a
		// later scan still re-processes the files that changed in it.
		if cur.Offset == 0 && cur.PairStart == "" && cfg.IncrementalSync && !threshold.IsZero() {
			if scanned, ok := cachedLibraryLastScan(libCache, lib.LibraryID); ok && scanned.Before(threshold) {
				logInfo(fmt.Sprintf(
					"nd-rating-sync: skipping user=%q library=%q – library unchanged since last sync (last_scan=%s threshold=%s)",
					u.Username, lib.LibraryID, scanned.UTC().Format(time.RFC3339), threshold.UTC().Format(time.RFC3339)))
				cur.User++
				cur.Offset = 0
				cur.PairStart = ""
				continue
			}
		}

		// Resolve the library's sandbox mount and index its files by (size,
		// suffix). Navidrome does not give plugins an openable path for a song
		// (the Subsonic `path` is a synthesized fake by default), so the only
		// way to locate a file is to walk the mount the host provides. A
		// failure here is non-fatal and the pair is skipped WITHOUT saving the
		// threshold – nothing was processed, so the next run retries from the
		// same baseline.
		index, indexOK := cachedFileIndex(indexCache, lib.LibraryID)
		if !indexOK {
			cur.User++
			cur.Offset = 0
			cur.PairStart = ""
			continue
		}

		if cur.PairStart == "" {
			cur.PairStart = time.Now().UTC().Format(time.RFC3339Nano)
		}

		next, pairDone := processPairChunk(lib, u, cfg, cur, threshold, deadline, index)
		cur = next
		if !pairDone {
			logTrace(fmt.Sprintf("nd-rating-sync: runSyncChunk stop, deadline hit lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			return cur, false // deadline hit (or fetch failed) mid-pair
		}

		// Pair finished: persist the threshold captured when the pair started.
		if cfg.IncrementalSync && !cfg.DryRun {
			if ps, err := time.Parse(time.RFC3339Nano, cur.PairStart); err == nil {
				saveLastSynced(lib.LibraryID, u.Username, ps)
			}
		}

		cur.User++
		cur.Offset = 0
		cur.PairStart = ""
	}
}

// processPairChunk processes songs for a single (library, user) pair starting
// at cur.Offset until the deadline elapses or the pair's songs are exhausted.
// It returns the advanced cursor and whether the pair is complete. The deadline
// is checked after each processed song, so at least one song is handled per
// call (provided the deadline had not already passed on entry, which
// runSyncChunk guarantees) – this is what makes the continuation chain
// terminate.
//
// A page-fetch failure returns pairDone=false without advancing past the failed
// page, so the next run retries the same offset; the cursor already points at
// the first unprocessed song.
func processPairChunk(lib libraryConfig, u userConfig, cfg pluginConfig, cur syncCursor, threshold time.Time, deadline time.Time, index map[string][]fileEntry) (syncCursor, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk start lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
	if cfg.DryRun {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] user=%q – no ratings will be written", u.Username))
	}
	if u.ClearRatingIfUntagged && u.SkipAlreadyRated {
		logWarn(fmt.Sprintf(
			"nd-rating-sync: user=%q has clear_rating_if_untagged=true but skip_already_rated=true – "+
				"songs already rated in Navidrome will be skipped before the file is read and their ratings will NOT be cleared",
			u.Username))
	}
	logInfo(fmt.Sprintf(
		"nd-rating-sync: syncing user=%q library=%q from offset=%d skip_already_rated=%v clear_rating_if_untagged=%v dry_run=%v tag_order=%q incremental_threshold=%s",
		u.Username, lib.LibraryID, cur.Offset, u.SkipAlreadyRated, u.ClearRatingIfUntagged, cfg.DryRun, u.RatingTagOrder, formatThreshold(threshold)))

	var tally syncTally
	for {
		pageOffset := (cur.Offset / songPageSize) * songPageSize
		skip := cur.Offset - pageOffset

		page, more, err := fetchSongPage(u.Username, lib.LibraryID, pageOffset, songPageSize)
		if err != nil {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: fetching songs for user=%q library=%q at offset=%d failed: %q – will retry next run",
				u.Username, lib.LibraryID, pageOffset, err.Error()))
			tally.log(u.Username, lib.LibraryID, cfg.DryRun)
			return cur, false
		}

		// Resume position is at/past the end of this page (only happens when a
		// page came back shorter than expected). Advance or finish.
		if skip >= len(page) {
			if !more {
				logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, no more lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
				tally.log(u.Username, lib.LibraryID, cfg.DryRun)
				return cur, true
			}
			cur.Offset = pageOffset + songPageSize
			continue
		}

		for i := skip; i < len(page); i++ {
			processSong(u, cfg, page[i], threshold, index, &tally)
			cur.Offset = pageOffset + i + 1
			// Check the deadline only every deadlineCheckEvery songs so the
			// hot loop does not hammer the WASI clock import. Reducing the
			// rate also reduces the surface area for the host-side clock
			// panic we have seen in production (see callBudget docs).
			if (i-skip+1)%deadlineCheckEvery == 0 && time.Now().After(deadline) {
				logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk stop, deadline reached lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
				tally.log(u.Username, lib.LibraryID, cfg.DryRun)
				return cur, false
			}
		}

		if !more {
			logTrace(fmt.Sprintf("nd-rating-sync: processPairChunk done lib=%q user=%q, offsett=%q, deadline=%q", cur.Lib, cur.User, cur.Offset, deadline))
			tally.log(u.Username, lib.LibraryID, cfg.DryRun)
			return cur, true
		}
		// Page was full; cur.Offset is already at the next page boundary.
	}
}

// syncTally accumulates per-chunk outcome counts for a single summary log line.
type syncTally struct {
	rated, cleared, wouldRate, wouldClear        int
	skippedRated, skippedNoTag, skippedUnchanged int
	skippedUnreadable, errored                   int
}

func (t syncTally) log(username, libraryID string, dryRun bool) {
	if dryRun {
		logInfo(fmt.Sprintf(
			"nd-rating-sync: [DRY RUN] chunk done user=%q library=%q – would_rate=%d would_clear=%d skipped_already_rated=%d skipped_unchanged=%d skipped_no_tag=%d skipped_unreadable=%d",
			username, libraryID, t.wouldRate, t.wouldClear, t.skippedRated, t.skippedUnchanged, t.skippedNoTag, t.skippedUnreadable))
		return
	}
	logInfo(fmt.Sprintf(
		"nd-rating-sync: chunk done user=%q library=%q – rated=%d cleared=%d skipped_already_rated=%d skipped_unchanged=%d skipped_no_tag=%d skipped_unreadable=%d errors=%d",
		username, libraryID, t.rated, t.cleared, t.skippedRated, t.skippedUnchanged, t.skippedNoTag, t.skippedUnreadable, t.errored))
}

// processSong applies the rating pipeline to one song: skip-if-already-rated,
// locate the real file under the library mount, skip-if-unchanged (incremental),
// read+parse the file, then write or clear the rating. Outcomes are accumulated
// into tally. A file that cannot be located, read, or parsed is treated as
// fileUnreadable – never as "untagged" – so clear_rating_if_untagged can never
// wipe a rating on a transient I/O error or an unmatched file.
func processSong(u userConfig, cfg pluginConfig, s subsonicSong, threshold time.Time, index map[string][]fileEntry, tally *syncTally) {
	logTrace(fmt.Sprintf("nd-rating-sync: processSong start song=%q, threshold=%q", s.ID, threshold))
	if u.SkipAlreadyRated && s.UserRating > 0 {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, already rated song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – already rated (%d stars in Navidrome)", s.Title, s.UserRating))
		tally.skippedRated++
		return
	}

	// Locate the file under the library mount. Navidrome's Subsonic `path`
	// field is a synthesized fake by default (see helpers.fakePath in the
	// server), so we cannot open it directly; instead we match on the
	// reported byte size + suffix that the host's scanner stored.
	// A missing or ambiguous match is treated as unreadable – never as
	// "no tag found" – so clear_rating_if_untagged can never wipe a rating
	// for a file we could not positively identify on disk.
	entry, found := matchFile(index, s)
	if !found {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, ambigous file song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: no unique file for %q (size=%d suffix=%q) – skipping",
			s.Title, s.Size, s.Suffix))
		tally.skippedUnreadable++
		return
	}

	if !threshold.IsZero() && entry.mtime.Before(threshold) {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no change song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf(
			"nd-rating-sync: skipping %q – unchanged since last scan (mtime=%s)",
			s.Title, entry.mtime.Format(time.RFC3339)))
		tally.skippedUnchanged++
		return
	}

	stars, result := extractStarsFromFile(entry.path, s.Suffix, u.RatingTagOrder)
	switch result {
	case fileUnreadable:
		// I/O error, unsupported extension, or parse panic. Never clear here —
		// clearing on a transient read failure would corrupt the user's
		// existing Navidrome rating. The warning was already logged inside
		// extractStarsFromFile; just count and move on.
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, file unreadable song=%q, threshold=%q", s.ID, threshold))
		tally.skippedUnreadable++
		return
	case tagAbsent:
		if !u.ClearRatingIfUntagged {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, no tag song=%q, threshold=%q", s.ID, threshold))
			tally.skippedNoTag++
			return
		}
		if cfg.DryRun {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, not allowed to clear song=%q, threshold=%q", s.ID, threshold))
			logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would clear rating for %q (no tag found)", s.Title))
			tally.wouldClear++
			return
		}
		if err := setRating(u.Username, s.ID, 0); err != nil {
			logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q, threshold=%q", s.ID, threshold))
			logWarn(fmt.Sprintf(
				"nd-rating-sync: setRating(0) failed for %q (id=%q): %v", s.Title, s.ID, err))
			tally.errored++
			return
		}
		logTrace(fmt.Sprintf("nd-rating-sync: processSong done, file unreadable song=%q, threshold=%q", s.ID, threshold))
		logDebug(fmt.Sprintf("nd-rating-sync: cleared rating for %q (no tag found)", s.Title))
		tally.cleared++
		return
	}

	// result == tagFound
	if cfg.DryRun {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, done song=%q, threshold=%q", s.ID, threshold))
		logInfo(fmt.Sprintf("nd-rating-sync: [DRY RUN] would rate %q → %d stars", s.Title, stars))
		tally.wouldRate++
		return
	}
	if err := setRating(u.Username, s.ID, stars); err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: processSong stop, rating failed song=%q, threshold=%q", s.ID, threshold))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: setRating failed for %q (id=%q): %v", s.Title, s.ID, err))
		tally.errored++
		return
	}
	logTrace(fmt.Sprintf("nd-rating-sync: setRating done song=%q, threshold=%q", s.ID, threshold))
	logDebug(fmt.Sprintf("nd-rating-sync: rated %q → %d stars", s.Title, stars))
	tally.rated++
}

// formatThreshold renders a threshold timestamp for log output, with a
// distinct marker when no threshold has been recorded yet.
func formatThreshold(t time.Time) string {
	if t.IsZero() {
		return "(none – full scan)"
	}
	return t.UTC().Format(time.RFC3339)
}

// ─── File reading ─────────────────────────────────────────────────────────────

// fileReadResult tells the caller whether the file was readable and parseable.
// It exists so a transient I/O error or unsupported extension can never be
// confused with "no rating tag found", which would otherwise cause
// clear_rating_if_untagged to wipe the user's existing Navidrome rating.
type fileReadResult int

const (
	tagFound       fileReadResult = iota // a recognised rating tag was extracted
	tagAbsent                            // file was read and parsed; no recognised tag
	fileUnreadable                       // I/O error, unsupported extension, or container parse failure
)

// maxMetadataReadBytes is the per-format upper bound on bytes the metadata
// extractors will pull into memory from any single file. Each format reads
// only its header + tag-bearing region (using Seek to jump past audio data),
// so the actual size is normally a few KiB; the cap exists as a safety net
// against pathological/hostile files (huge embedded artwork, /dev/zero,
// corrupt-but-readable size fields).
//
// 16 MiB covers legitimate cases like multi-MiB embedded cover art in FLAC
// PICTURE blocks or MP4 udta atoms. A file whose metadata genuinely exceeds
// this is reported as fileUnreadable so clear_rating_if_untagged cannot
// wipe a rating for a file we did not fully read.
const maxMetadataReadBytes = 16 * 1024 * 1024

// extractStarsFromFile opens the audio file at path and returns a 1–5 star
// rating using the tag formats in tagOrder for priority. It dispatches to a
// format-specific extractor that reads ONLY the metadata-bearing portion of
// the file — never the audio body — so per-song I/O is bounded by
// maxMetadataReadBytes regardless of how big the file is on disk. The
// fileReadResult disambiguates "no tag found" (safe to clear) from "could
// not read" (must skip — clearing on I/O errors would corrupt user state).
func extractStarsFromFile(path, suffix string, tagOrder []string) (int, fileReadResult) {
	logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile start path=%q, suffix=%q", path, suffix))
	ext := strings.ToLower(suffix)
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	if !isSupportedExt(ext) {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, unsupported file path=%q, suffix=%q", path, suffix))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: skipping %q – supported formats are MP3, FLAC, Ogg, Opus, WAV, DSF, M4A/AAC and WMA (got .%q)", path, ext))
		return 0, fileUnreadable
	}

	data, ok := readAudioMetadata(path, ext)
	if !ok {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, file unreadable path=%q, suffix=%q", path, suffix))
		return 0, fileUnreadable
	}

	stars, ok, supported := dispatchParser(path, ext, data, tagOrder)
	if !supported {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, not supported path=%q, suffix=%q", path, suffix))
		return 0, fileUnreadable
	}
	if ok {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile done, rating found path=%q, suffix=%q", path, suffix))
		logDebug(fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		return stars, tagFound
	}
	logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile done, no rating path=%q, suffix=%q", path, suffix))
	logDebug(fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
	return 0, tagAbsent
}

// readAudioMetadata opens path and dispatches to the per-format extractor for
// ext. The extractors read only the file's metadata-bearing region (header
// + tag chunk / atom / block) using Seek to skip audio bodies, so per-song
// I/O is bounded by maxMetadataReadBytes — independent of total file size.
// Returns the synthesised byte slice the existing format parsers can walk.
func readAudioMetadata(path, ext string) ([]byte, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: readAudioMetadata start path=%q, etx=%q", path, ext))
	f, err := os.Open(path)
	if err != nil {
		// Log only the path — the raw OS error ("permission denied" vs
		// "no such file or directory") would let an admin who can plant
		// a symlink in the music tree probe arbitrary paths' existence
		// via plugin warnings. Path alone is enough for diagnostics.
		logTrace(fmt.Sprintf("nd-rating-sync: readAudioMetadata stop, cannot open path=%q, etx=%q", path, ext))
		logWarn(fmt.Sprintf("nd-rating-sync: cannot open %q (skipping)", path))
		logDebug(fmt.Sprintf("nd-rating-sync: open %q error: %q", path, err.Error()))
		return nil, false
	}
	defer f.Close()

	var (
		data []byte
		eerr error
	)
	switch ext {
	case "mp3":
		data, eerr = extractID3v2Metadata(f)
	case "flac":
		data, eerr = extractFLACMetadata(f)
	case "ogg", "oga", "opus":
		data, eerr = extractOggMetadata(f)
	case "wav":
		data, eerr = extractWAVMetadata(f)
	case "dsf":
		data, eerr = extractDSFMetadata(f)
	case "m4a", "aac", "mp4":
		data, eerr = extractM4AMetadata(f)
	case "wma":
		data, eerr = extractWMAMetadata(f)
	default:
		// extractStarsFromFile pre-filters via isSupportedExt, so this is
		// only reached if a new extension was added there but not here.
		logTrace(fmt.Sprintf("nd-rating-sync: readAudioMetadata stop, unsupported extension path=%q, etx=%q", path, ext))
		return nil, false
	}
	if eerr != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: readAudioMetadata stop, cannot read path=%q, etx=%q", path, ext))
		logWarn(fmt.Sprintf("nd-rating-sync: cannot read %q (skipping)", path))
		logDebug(fmt.Sprintf("nd-rating-sync: read %q error: %q", path, eerr.Error()))
		return nil, false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: readAudioMetadata done path=%q, etx=%q", path, ext))
	return data, true
}

// dispatchParser routes data to the right container parser, recovering from
// any panic the parser raises on hostile input so a single bad file cannot
// abort the whole sync. Returns (stars, tagFound, formatSupported).
func dispatchParser(path, ext string, data []byte, tagOrder []string) (stars int, ok, supported bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser start path=%q, etx=%q", path, ext))
	supported = true
	defer func() {
		if r := recover(); r != nil {
			logWarn(fmt.Sprintf(
				"nd-rating-sync: panic parsing %q (%q): %v – treating as unreadable", path, ext, r))
			stars, ok, supported = 0, false, false
		}
	}()

	switch ext {
	case "mp3":
		stars, ok = parseID3v2Rating(data, tagOrder)
	case "flac":
		stars, ok = parseFLACRating(data, tagOrder)
	case "ogg", "oga", "opus":
		stars, ok = parseOggVorbisRating(data, tagOrder)
	case "wav":
		stars, ok = parseWAVRating(data, tagOrder)
	case "dsf":
		stars, ok = parseDSFRating(data, tagOrder)
	case "m4a", "aac", "mp4":
		stars, ok = parseM4ARating(data, tagOrder)
	case "wma":
		stars, ok = parseWMARating(data, tagOrder)
	default:
		logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser stop, unsupported extension path=%q, etx=%q", path, ext))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: skipping %q – supported formats are MP3, FLAC, Ogg, Opus, WAV, DSF, M4A/AAC and WMA (got .%q)", path, ext))
		supported = false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser done path=%q, etx=%q", path, ext))
	return stars, ok, supported
}
