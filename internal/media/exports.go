package media

import (
	"fmt"
	"os"
)

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
	switch ext {
	case "mp3":
		return extractID3v2Metadata(f)
	case "flac":
		return extractFLACMetadata(f)
	case "ogg", "oga", "opus":
		return extractOggMetadata(f)
	case "wav":
		return extractWAVMetadata(f)
	case "dsf":
		return extractDSFMetadata(f)
	case "m4a", "mp4":
		return extractM4AMetadata(f)
	case "aac":
		return extractAACMetadata(f)
	case "wma":
		return extractWMAMetadata(f)
	}
	return nil, fmt.Errorf("unsupported extension %q", ext)
}

// ParseRating returns the rating in data (as returned by ExtractMetadata)
// using tagOrder for priority. found=false with a nil error means the file
// carries no recognised rating; an error means the tag is corrupt or
// unsupported and the file must be treated as unreadable, never as untagged.
func ParseRating(ext string, data []byte, path string, tagOrder []string) (stars int, found bool, err error) {
	switch ext {
	case "mp3":
		return ratingFromID3v2(data, path, tagOrder)
	case "flac":
		return ratingFromFLAC(data, path, tagOrder)
	case "ogg", "oga", "opus":
		return ratingFromOgg(data, path, tagOrder)
	case "wav":
		return ratingFromWAV(data, path, tagOrder)
	case "dsf":
		return ratingFromDSF(data, path, tagOrder)
	case "m4a", "mp4":
		return ratingFromM4A(data, path, tagOrder)
	case "aac":
		return ratingFromAAC(data, path, tagOrder)
	case "wma":
		return ratingFromWMA(data, path, tagOrder)
	}
	return 0, false, fmt.Errorf("unsupported extension %q", ext)
}
