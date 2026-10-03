package scanner

import "strconv"

func isSupportedExt(ext string) bool {
	switch ext {
	case "mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4", "wma":
		return true
	}
	return false
}

func sizeKey(size int64, ext string) string {
	return strconv.FormatInt(size, 10) + ":" + ext
}
