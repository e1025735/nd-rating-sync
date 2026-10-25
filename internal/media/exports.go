package media

import (
	"fmt"
	"os"
)

// ExtractMetadata reads only the tag-bearing region of f for the container
// named by ext (lower-case, no dot) and returns it in a form ParseRating
// understands. A nil slice with a nil error means the file does not carry the
// container's magic and is treated as untagged; an error means the tag region
// could not be read and the file must be treated as unreadable.
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
	case "m4a", "aac", "mp4":
		return extractM4AMetadata(f)
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
	case "m4a", "aac", "mp4":
		stars, found = parseM4ARating(data, path, tagOrder)
		return stars, found, nil
	case "wma":
		stars, found = parseWMARating(data, path, tagOrder)
		return stars, found, nil
	}
	return 0, false, fmt.Errorf("unsupported extension %q", ext)
}
