//go:build !tinygo

package scanner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSizeKey_CombinesSizeAndExt(t *testing.T) {
	assert.Equal(t, "123:mp3", sizeKey(123, "mp3"))
	assert.NotEqual(t, sizeKey(123, "mp3"), sizeKey(123, "flac"))
}

func TestIsSupportedExt(t *testing.T) {
	for _, ext := range []string{"mp3", "flac", "ogg", "oga", "opus", "wav", "dsf", "m4a", "aac", "mp4", "wma"} {
		assert.True(t, isSupportedExt(ext), ext)
	}
	for _, ext := range []string{"", "txt", "jpg", "nfo", "MP3"} {
		assert.False(t, isSupportedExt(ext), ext)
	}
}
