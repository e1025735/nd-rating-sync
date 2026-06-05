package main

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

func buildAtom(typ string, body []byte) []byte {
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[:4], uint32(8+len(body)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

// buildFullAtom prepends the 4-byte version+flags required by MP4 FullBoxes.
func buildFullAtom(typ string, body []byte) []byte {
	return buildAtom(typ, append([]byte{0, 0, 0, 0}, body...))
}

// buildFreeformAtom constructs a "----" atom with mean, name, and data children.
func buildFreeformAtom(mean, name, value string) []byte {
	meanAtom := buildFullAtom("mean", []byte(mean))
	nameAtom := buildFullAtom("name", []byte(name))
	// data FullBox: 4-byte type (1 = UTF-8) + 4-byte locale + value bytes.
	dataBody := append([]byte{0, 0, 0, 1, 0, 0, 0, 0}, []byte(value)...)
	dataAtom := buildAtom("data", dataBody)
	return buildAtom("----", append(append(meanAtom, nameAtom...), dataAtom...))
}

// buildM4A wraps freeform atoms in a minimal moov→udta→meta→ilst hierarchy.
// Each entry in freeformTags is {"atomName": "value"} using "com.apple.iTunes"
// as the mean.
func buildM4A(freeformTags map[string]string) []byte {
	var ilstBody []byte
	for name, value := range freeformTags {
		ilstBody = append(ilstBody, buildFreeformAtom("com.apple.iTunes", name, value)...)
	}
	ilst := buildAtom("ilst", ilstBody)
	meta := buildAtom("meta", append([]byte{0, 0, 0, 0}, ilst...)) // meta FullBox
	udta := buildAtom("udta", meta)
	moov := buildAtom("moov", udta)
	ftyp := buildAtom("ftyp", []byte("M4A \x00\x00\x00\x00"))
	return append(ftyp, moov...)
}

// ─── invalid / edge cases ─────────────────────────────────────────────────────

func TestParseM4ARating_EmptySlice(t *testing.T) {
	_, ok := parseM4ARating([]byte{}, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseM4ARating_NoMoov(t *testing.T) {
	data := buildAtom("ftyp", []byte("M4A \x00\x00\x00\x00"))
	_, ok := parseM4ARating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseM4ARating_NoIlst(t *testing.T) {
	data := buildM4A(nil)
	_, ok := parseM4ARating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseM4ARating_TruncatedAtom(t *testing.T) {
	// atom header claims 100 bytes but only 8 present
	_, ok := parseM4ARating([]byte{0, 0, 0, 100, 'm', 'o', 'o', 'v'}, []string{"MediaMonkey"})
	assert.False(t, ok)
}

// ─── MediaMonkey (FMPS_Rating) ────────────────────────────────────────────────

func TestParseM4ARating_MediaMonkeyCanonicalValues(t *testing.T) {
	cases := []struct {
		fmps string
		want int
	}{
		{"0.2", 1}, {"0.4", 2}, {"0.6", 3}, {"0.8", 4}, {"1.0", 5},
	}
	for _, tc := range cases {
		data := buildM4A(map[string]string{"FMPS_Rating": tc.fmps})
		stars, ok := parseM4ARating(data, []string{"MediaMonkey"})
		assert.True(t, ok, "FMPS_Rating=%s", tc.fmps)
		assert.Equal(t, tc.want, stars, "FMPS_Rating=%s", tc.fmps)
	}
}

func TestParseM4ARating_MediaMonkeyZeroIsUnrated(t *testing.T) {
	data := buildM4A(map[string]string{"FMPS_Rating": "0.0"})
	_, ok := parseM4ARating(data, []string{"MediaMonkey"})
	assert.False(t, ok)
}

func TestParseM4ARating_MediaMonkeyCaseInsensitiveName(t *testing.T) {
	data := buildM4A(map[string]string{"FMPS_RATING": "0.6"})
	stars, ok := parseM4ARating(data, []string{"MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 3, stars)
}

// ─── foobar2000 (RATING uppercase) ───────────────────────────────────────────

func TestParseM4ARating_foobar2000CanonicalValues(t *testing.T) {
	for n := 1; n <= 5; n++ {
		data := buildM4A(map[string]string{"RATING": string(rune('0' + n))})
		stars, ok := parseM4ARating(data, []string{"foobar2000"})
		assert.True(t, ok, "RATING=%d", n)
		assert.Equal(t, n, stars, "RATING=%d", n)
	}
}

func TestParseM4ARating_foobar2000ZeroIsUnrated(t *testing.T) {
	data := buildM4A(map[string]string{"RATING": "0"})
	_, ok := parseM4ARating(data, []string{"foobar2000"})
	assert.False(t, ok)
}

// ─── iTunes (rating lowercase) ────────────────────────────────────────────────

func TestParseM4ARating_iTunesCanonicalValues(t *testing.T) {
	cases := []struct {
		val  string
		want int
	}{
		{"20", 1}, {"40", 2}, {"60", 3}, {"80", 4}, {"100", 5},
	}
	for _, tc := range cases {
		data := buildM4A(map[string]string{"rating": tc.val}) // lowercase = iTunes
		stars, ok := parseM4ARating(data, []string{"iTunes"})
		assert.True(t, ok, "rating=%s", tc.val)
		assert.Equal(t, tc.want, stars, "rating=%s", tc.val)
	}
}

func TestParseM4ARating_iTunesZeroIsUnrated(t *testing.T) {
	data := buildM4A(map[string]string{"rating": "0"})
	_, ok := parseM4ARating(data, []string{"iTunes"})
	assert.False(t, ok)
}

// ─── tag order / priority ─────────────────────────────────────────────────────

func TestParseM4ARating_TagOrderFMPSBeatsRATING(t *testing.T) {
	// Build a file with both FMPS_Rating (→2 stars) and RATING (→5 stars).
	// buildM4A uses a map so iteration order is irrelevant — both end up in ilst.
	data := buildM4A(map[string]string{"FMPS_Rating": "0.4", "RATING": "5"})

	stars, ok := parseM4ARating(data, []string{"MediaMonkey", "foobar2000"})
	assert.True(t, ok)
	assert.Equal(t, 2, stars)

	stars, ok = parseM4ARating(data, []string{"foobar2000", "MediaMonkey"})
	assert.True(t, ok)
	assert.Equal(t, 5, stars)
}

func TestParseM4ARating_EmptyTagOrderNeverMatches(t *testing.T) {
	data := buildM4A(map[string]string{"FMPS_Rating": "0.6"})
	_, ok := parseM4ARating(data, []string{})
	assert.False(t, ok)
}

// ─── extractor (Phase 1 partial reads) ────────────────────────────────────────

// TestExtractM4AMetadata_SkipsMdat proves the MP4 extractor walks top-level
// atoms and Seeks past `mdat` — the audio data, often the overwhelming
// majority of an MP4's bytes — when `moov` comes after it (the layout
// ffmpeg / iTunes produce by default for non-fast-start output). The
// sentinel lives inside the `mdat` body; if the extractor reads it instead
// of seeking past it would show up in the returned synth.
func TestExtractM4AMetadata_SkipsMdat(t *testing.T) {
	dir := t.TempDir()

	// buildM4A returns ftyp(16 bytes) + moov. Slice off the ftyp to get a
	// standalone moov atom we can place AFTER mdat.
	full := buildM4A(map[string]string{"FMPS_Rating": "0.6"}) // 3 stars
	moov := full[16:]
	ftyp := buildAtom("ftyp", []byte("M4A \x00\x00\x00\x00"))
	mdat := buildAtom("mdat", junkAudio())

	var content []byte
	content = append(content, ftyp...)
	content = append(content, mdat...)
	content = append(content, moov...)

	path := writeBinFile(t, dir, "song.m4a", content)

	data := extractedBytes(t, path, "m4a")
	if bytesContains(data, sentinel) {
		t.Fatalf("MP4 extractor must Seek past the mdat atom (sentinel found in %d-byte output)", len(data))
	}

	stars, result := extractStarsFromFile(path, "m4a", []string{"MediaMonkey"})
	assert.Equal(t, tagFound, result)
	assert.Equal(t, 3, stars)
}

// bytesContains is a tiny local helper so this file doesn't need to import
// the standard "bytes" package just for the regression check.
func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j, b := range needle {
			if haystack[i+j] != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
