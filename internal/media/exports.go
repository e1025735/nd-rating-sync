package media

import (
	"fmt"
	"os"
	"sort"
)

// format is how one container is read: extract pulls only the tag-bearing
// region out of the file, parse reads the rating from what extract returned.
type format struct {
	extract func(f *os.File) ([]byte, error)
	parse   func(data []byte, path string, tagOrder []string) (int, bool, error)
}

// formats maps every supported extension (lower-case, no dot) to its reader.
// It is the one list of supported formats: adding an entry here is all the
// scanner needs.
var formats = map[string]format{
	"mp3":  {extractID3v2Metadata, ratingFromID3v2},
	"flac": {extractFLACMetadata, ratingFromFLAC},
	"ogg":  {extractOggMetadata, ratingFromOgg},
	"oga":  {extractOggMetadata, ratingFromOgg},
	"opus": {extractOggMetadata, ratingFromOgg},
	"wav":  {extractWAVMetadata, ratingFromWAV},
	"dsf":  {extractDSFMetadata, ratingFromDSF},
	"m4a":  {extractM4AMetadata, ratingFromM4A},
	"mp4":  {extractM4AMetadata, ratingFromM4A},
	"aac":  {extractAACMetadata, ratingFromAAC},
	"wma":  {extractWMAMetadata, ratingFromWMA},
}

// Supported reports whether ext (lower-case, no dot) is a format the plugin
// reads.
func Supported(ext string) bool {
	_, ok := formats[ext]
	return ok
}

// SupportedExtensions lists the supported extensions in sorted order, for
// messages.
func SupportedExtensions() []string {
	exts := make([]string, 0, len(formats))
	for ext := range formats {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// ExtractMetadata reads only the tag-bearing region of f for the container
// named by ext (lower-case, no dot) and returns it in a form ParseRating
// understands.
//
// The rule for every format: a file is untagged only when its container was
// read to a clean end and holds no rating tag. An error means the container
// or its tag region could not be read — a wrong magic (the file is not what
// its extension says), a size that does not fit the file, a cut-off file —
// and the file must be treated as unreadable, so clear_rating_if_untagged can
// never wipe a rating because of it. The one nil slice with a nil error is an
// MP3 without a leading ID3v2 tag, which carries no supported rating; an MP4
// whose moov has no udta also returns nil, which parses as untagged. A .aac
// file is sniffed: an ID3v2-tagged ADTS stream is read like an MP3 tag, an
// MP4 file like an .m4a, and an ADTS stream without ID3v2 is unreadable.
func ExtractMetadata(ext string, f *os.File) ([]byte, error) {
	fm, ok := formats[ext]
	if !ok {
		return nil, fmt.Errorf("unsupported extension %q", ext)
	}
	return fm.extract(f)
}

// ParseRating returns the rating in data (as returned by ExtractMetadata)
// using tagOrder for priority. found=false with a nil error means the file
// carries no recognised rating; an error means the tag is corrupt or
// unsupported and the file must be treated as unreadable, never as untagged.
func ParseRating(ext string, data []byte, path string, tagOrder []string) (stars int, found bool, err error) {
	fm, ok := formats[ext]
	if !ok {
		return 0, false, fmt.Errorf("unsupported extension %q", ext)
	}
	return fm.parse(data, path, tagOrder)
}
