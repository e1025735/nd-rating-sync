package media

import (
	"os"
)

func ParseFLACVorbisComments(data []byte) (map[string][]string, error) {
	cmts, err := parseFLACVorbisComments(data)
	if err != nil || cmts == nil {
		return nil, err
	}
	out := make(map[string][]string, len(cmts))
	for k, v := range cmts {
		out[k] = append([]string(nil), v...)
	}
	return out, nil
}

func ParseVorbisCommentBlock(body []byte) (map[string][]string, error) {
	cmts, err := parseVorbisCommentBlock(body)
	if err != nil || cmts == nil {
		return nil, err
	}
	out := make(map[string][]string, len(cmts))
	for k, v := range cmts {
		out[k] = append([]string(nil), v...)
	}
	return out, nil
}

func RatingFromVorbisComments(cmts map[string][]string, path string, tagOrder []string) (int, bool) {
	adapter := make(vorbisComments, len(cmts))
	for k, v := range cmts {
		adapter[k] = append([]string(nil), v...)
	}
	return ratingFromVorbisComments(adapter, path, tagOrder)
}

func ParseFLACRating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseFLACRating(data, path, tagOrder)
}

func ExtractFLACMetadata(f *os.File) ([]byte, error) {
	return extractFLACMetadata(f)
}

func ParseOggVorbisRating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseOggVorbisRating(data, path, tagOrder)
}

func ExtractOggPackets(data []byte, maxPackets int) ([][]byte, error) {
	return extractOggPackets(data, maxPackets)
}

func ExtractOggMetadata(f *os.File) ([]byte, error) {
	return extractOggMetadata(f)
}

func ParseWAVRating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseWAVRating(data, path, tagOrder)
}

func ExtractWAVMetadata(f *os.File) ([]byte, error) {
	return extractWAVMetadata(f)
}

func ParseDSFRating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseDSFRating(data, path, tagOrder)
}

func ExtractDSFMetadata(f *os.File) ([]byte, error) {
	return extractDSFMetadata(f)
}

func ParseM4ARating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseM4ARating(data, path, tagOrder)
}

func ExtractM4AMetadata(f *os.File) ([]byte, error) {
	return extractM4AMetadata(f)
}

func ParseWMARating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseWMARating(data, path, tagOrder)
}

func ParseASFExtContentDesc(body []byte, path string, tagOrder []string) (int, bool) {
	return parseASFExtContentDesc(body, path, tagOrder)
}

func DecodeUTF16LE(b []byte) string {
	return decodeUTF16LE(b)
}

func ExtractWMAMetadata(f *os.File) ([]byte, error) {
	return extractWMAMetadata(f)
}

func ParseID3v2Rating(data []byte, path string, tagOrder []string) (int, bool) {
	return parseID3v2Rating(data, path, tagOrder)
}

func ExtractID3v2Metadata(f *os.File) ([]byte, error) {
	return extractID3v2Metadata(f)
}

func ReadID3v2TagAt(f *os.File, off int64) ([]byte, error) {
	return readID3v2TagAt(f, off)
}

func FmpsToStars(s string) (int, bool) {
	return fmpsToStars(s)
}

func RatingIntToStars(s string) (int, bool) {
	return ratingIntToStars(s)
}

func RatingMusicBeeToStars(s string) (int, bool) {
	return ratingMusicBeeToStars(s)
}

func PopmWMPToStars(b byte) int {
	return popmWMPToStars(b)
}

func PopmITunesToStars(b byte) int {
	return popmITunesToStars(b)
}
