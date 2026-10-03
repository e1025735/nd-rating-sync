package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/e1025735/nd-rating-sync/internal/media"
)

// ExtractStarsFromFile opens the audio file at path and returns a 1–5 star
// rating using the tag formats in tagOrder for priority.
func ExtractStarsFromFile(path string, suffix string, tagOrder []string) (int, FileReadResult) {
	logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile start path=%q, suffix=%q", path, suffix))
	ext := strings.ToLower(suffix)
	if ext == "" {
		ext = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	if !isSupportedExt(ext) {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, unsupported file path=%q, suffix=%q", path, suffix))
		logWarn(fmt.Sprintf(
			"nd-rating-sync: skipping %q – supported formats are MP3, FLAC, Ogg, Opus, WAV, DSF, M4A/AAC and WMA (got .%q)", path, ext))
		return 0, FileUnreadable
	}

	data, ok := ReadAudioMetadata(path, ext)
	if !ok {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, file unreadable path=%q, suffix=%q", path, suffix))
		return 0, FileUnreadable
	}

	stars, ok, supported := DispatchParser(path, ext, data, tagOrder)
	if !supported {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile stop, not supported path=%q, suffix=%q", path, suffix))
		return 0, FileUnreadable
	}
	if ok {
		logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile done, rating found path=%q, suffix=%q", path, suffix))
		logDebug(fmt.Sprintf("nd-rating-sync: %q – found rating tag → %d stars", path, stars))
		return stars, TagFound
	}
	logTrace(fmt.Sprintf("nd-rating-sync: extractStarsFromFile done, no rating path=%q, suffix=%q", path, suffix))
	logDebug(fmt.Sprintf("nd-rating-sync: %q – no rating tag found", path))
	return 0, TagAbsent
}

func extractStarsFromFile(path string, suffix string, tagOrder []string) (int, FileReadResult) {
	return ExtractStarsFromFile(path, suffix, tagOrder)
}

func ReadAudioMetadata(path, ext string) ([]byte, bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: readAudioMetadata start path=%q, etx=%q", path, ext))
	f, err := os.Open(path)
	if err != nil {
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

func DispatchParser(path string, ext string, data []byte, tagOrder []string) (stars int, ok, supported bool) {
	logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser start path=%q, etx=%q", path, ext))
	supported = true
	defer func() {
		if r := recover(); r != nil {
			logWarn(fmt.Sprintf("nd-rating-sync: panic parsing %q (%q): %v – treating as unreadable", path, ext, r))
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
		logWarn(fmt.Sprintf("nd-rating-sync: skipping %q – supported formats are MP3, FLAC, Ogg, Opus, WAV, DSF, M4A/AAC and WMA (got .%q)", path, ext))
		supported = false
	}
	logTrace(fmt.Sprintf("nd-rating-sync: dispatchParser done path=%q, etx=%q", path, ext))
	return stars, ok, supported
}
