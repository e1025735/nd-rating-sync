package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e1025735/nd-rating-sync/internal/media"
)

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

// extractStarsFromFile opens the audio file at path and returns a 1–5 star
// rating using the tag formats in tagOrder for priority. It dispatches to a
// format-specific extractor that reads ONLY the metadata-bearing portion of
// the file — never the audio body — so per-song I/O is bounded by the
// package-level cap defined in internal/media/constants.go regardless of how
// large the audio stream is on disk. The fileReadResult disambiguates
// "no tag found" (safe to clear) from "could not read" (must skip —
// clearing on I/O errors would corrupt user state).
func extractStarsFromFile(path string, suffix string, tagOrder []string) (int, fileReadResult) {
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
// I/O is capped by the media-layer guard in internal/media/constants.go —
// independent of total file size. Returns the synthesised byte slice the
// existing format parsers can walk.
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
		data, eerr = media.ExtractID3v2Metadata(f)
	case "flac":
		data, eerr = media.ExtractFLACMetadata(f)
	case "ogg", "oga", "opus":
		data, eerr = media.ExtractOggMetadata(f)
	case "wav":
		data, eerr = media.ExtractWAVMetadata(f)
	case "dsf":
		data, eerr = media.ExtractDSFMetadata(f)
	case "m4a", "aac", "mp4":
		data, eerr = media.ExtractM4AMetadata(f)
	case "wma":
		data, eerr = media.ExtractWMAMetadata(f)
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
func dispatchParser(path string, ext string, data []byte, tagOrder []string) (stars int, ok, supported bool) {
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
		stars, ok = media.ParseID3v2Rating(data, path, tagOrder)
	case "flac":
		stars, ok = media.ParseFLACRating(data, path, tagOrder)
	case "ogg", "oga", "opus":
		stars, ok = media.ParseOggVorbisRating(data, path, tagOrder)
	case "wav":
		stars, ok = media.ParseWAVRating(data, path, tagOrder)
	case "dsf":
		stars, ok = media.ParseDSFRating(data, path, tagOrder)
	case "m4a", "aac", "mp4":
		stars, ok = media.ParseM4ARating(data, path, tagOrder)
	case "wma":
		stars, ok = media.ParseWMARating(data, path, tagOrder)
	default:
		logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser stop, unsupported extension path=%q, etx=%q", path, ext))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: skipping %q – supported formats are MP3, FLAC, Ogg, Opus, WAV, DSF, M4A/AAC and WMA (got .%q)", path, ext))
		supported = false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser done path=%q, etx=%q", path, ext))
	return stars, ok, supported
}
