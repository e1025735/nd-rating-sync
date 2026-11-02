//go:build !tinygo

package media

// Two-value forms of the ratingFromX parsers for tests that only check the
// rating: an error reads as "no rating", like found=false.

func parseID3v2Rating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromID3v2(data, path, tagOrder)
	return stars, ok
}

func parseFLACRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromFLAC(data, path, tagOrder)
	return stars, ok
}

func parseOggVorbisRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromOgg(data, path, tagOrder)
	return stars, ok
}

func parseWAVRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromWAV(data, path, tagOrder)
	return stars, ok
}

func parseDSFRating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromDSF(data, path, tagOrder)
	return stars, ok
}

func parseM4ARating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromM4A(data, path, tagOrder)
	return stars, ok
}

func parseWMARating(data []byte, path string, tagOrder []string) (int, bool) {
	stars, ok, _ := ratingFromWMA(data, path, tagOrder)
	return stars, ok
}
