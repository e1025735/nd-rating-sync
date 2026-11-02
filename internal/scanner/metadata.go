package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e1025735/nd-rating-sync/internal/logging"
	"github.com/e1025735/nd-rating-sync/internal/media"
)

// extractStarsFromFile opens the audio file at path and returns a 1–5 star
// rating using the tag formats in tagOrder for priority.
//
// Only TagAbsent may lead to a rating being cleared, so every doubt — the
// file cannot be opened, its tag region cannot be read, or the tag is
// corrupt or unsupported — is FileUnreadable.
func extractStarsFromFile(path string, suffix string, tagOrder []string) (int, FileReadResult) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile start path=%q, suffix=%q", path, suffix))
	ext := strings.ToLower(suffix)
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	if !media.Supported(ext) {
		logging.Trace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, unsupported file path=%q, suffix=%q", path, suffix))
		logging.Warn(fmt.Sprintf(
			"nd-rating-sync: skipping %q – unsupported file type %q (supported: %s)", path, ext, strings.Join(media.SupportedExtensions(), ", ")))
		return 0, FileUnreadable
	}

	data, ok := readAudioMetadata(path, ext)
	if !ok {
		logging.Trace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, file unreadable path=%q, suffix=%q", path, suffix))
		return 0, FileUnreadable
	}

	stars, result := parseStars(path, ext, data, tagOrder)
	switch result {
	case TagFound:
		logging.Trace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile done, rating found path=%q, suffix=%q", path, suffix))
	case TagAbsent:
		logging.Trace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile done, no rating path=%q, suffix=%q", path, suffix))
	default:
		logging.Trace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, tag not parseable path=%q, suffix=%q", path, suffix))
	}
	return stars, result
}

// readAudioMetadata reads only the tag-bearing region of the file (see
// media.ExtractMetadata). Failures are logged and reported as !ok.
func readAudioMetadata(path, ext string) ([]byte, bool) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: readAudioMetadata start path=%q, ext=%q", path, ext))
	f, err := os.Open(path)
	if err != nil {
		logging.Trace(fmt.Sprintf("nd-rating-sync: readAudioMetadata stop, cannot open path=%q, ext=%q", path, ext))
		logging.Warn(fmt.Sprintf("nd-rating-sync: cannot open %q (skipping)", path))
		logging.Debug(fmt.Sprintf("nd-rating-sync: open %q error: %q", path, err.Error()))
		return nil, false
	}
	defer f.Close()

	data, err := media.ExtractMetadata(ext, f)
	if err != nil {
		logging.Trace(fmt.Sprintf("nd-rating-sync: readAudioMetadata stop, cannot read path=%q, ext=%q", path, ext))
		logging.Warn(fmt.Sprintf("nd-rating-sync: cannot read the tags of %q (skipping)", path))
		logging.Debug(fmt.Sprintf("nd-rating-sync: read %q error: %q", path, err.Error()))
		return nil, false
	}
	logging.Trace(fmt.Sprintf("nd-rating-sync: readAudioMetadata done path=%q, ext=%q, bytes=%d", path, ext, len(data)))
	return data, true
}

// parseStars resolves the rating from the extracted tag region.
//
// The media parsers are written to be panic-free on any input (they are
// fuzzed). The recover below only protects native builds such as `go test`:
// TinyGo cannot recover panics on WebAssembly.
func parseStars(path, ext string, data []byte, tagOrder []string) (stars int, result FileReadResult) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: parseStars start path=%q, ext=%q", path, ext))
	defer func() {
		if r := recover(); r != nil {
			logging.Warn(fmt.Sprintf("nd-rating-sync: panic parsing %q (%q): %v – treating as unreadable", path, ext, r))
			stars, result = 0, FileUnreadable
		}
	}()

	stars, found, err := media.ParseRating(ext, data, path, tagOrder)
	switch {
	case errors.Is(err, media.ErrNotWholeStar):
		logging.Debug(fmt.Sprintf("nd-rating-sync: %q – the rating tag holds a half star or an out-of-range value", path))
		return 0, TagNotWholeStar
	case err != nil:
		logging.Trace(fmt.Sprintf("nd-rating-sync: parseStars stop, parse error path=%q, ext=%q", path, ext))
		// The reason (e.g. an FMPS_Rating outside 0.0–1.0) comes from the tag
		// itself, not from the OS, so it is safe to show in the warning.
		logging.Warn(fmt.Sprintf("nd-rating-sync: cannot parse the tags of %q (skipping): %q", path, err.Error()))
		return 0, FileUnreadable
	case found:
		logging.Trace(fmt.Sprintf("nd-rating-sync: parseStars done, rating found path=%q, ext=%q", path, ext))
		logging.Debug(fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		return stars, TagFound
	default:
		logging.Trace(fmt.Sprintf("nd-rating-sync: parseStars done, no rating path=%q, ext=%q", path, ext))
		logging.Debug(fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
		return 0, TagAbsent
	}
}
