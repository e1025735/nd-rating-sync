//go:build !tinygo

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFmpsToStars(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		want   int
	}{
		{"empty string", "", false, 0},
		{"zero bare", "0", false, 0},
		{"zero float", "0.0", false, 0},
		{"1 star canonical", "0.2", true, 1},
		{"2 stars canonical", "0.4", true, 2},
		{"3 stars canonical", "0.6", true, 3},
		{"4 stars canonical", "0.8", true, 4},
		{"5 stars canonical", "1.0", true, 5},
		{"tiny positive is not a whole star", "0.01", false, 0},
		{"half star 0.5 is unrated", "0.1", false, 0},
		{"half star 4.5 is unrated", "0.9", false, 0},
		{"half star 2.5 is unrated", "0.5", false, 0},
		{"float noise still whole", "0.60000002", true, 3},
		{"two decimals", "0.80", true, 4},
		{"leading/trailing whitespace", "  0.6  ", true, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := fmpsToStars(tc.input)
			assert.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// A value that is not a number or lies outside 0.0–1.0 is an error, never a
// rating: "4" (stars) or "80" (percent) must not become 5 stars.
func TestFmpsToStars_InvalidValuesAreErrors(t *testing.T) {
	for _, s := range []string{"abc", "0.6abc", "-0.5", "1.5", "4", "80", "NaN", "Inf", "1e10"} {
		stars, ok, err := fmpsToStars(s)
		assert.Error(t, err, s)
		assert.False(t, ok, s)
		assert.Equal(t, 0, stars, s)
	}
}

func TestPickRating_InvalidSourceBeforeAValidOneIsAnError(t *testing.T) {
	found := map[string]int{"WMP": 4}
	invalid := map[string]error{"FMPS": assert.AnError}

	stars, ok, err := pickRating([]string{"WMP", "FMPS"}, found, invalid, nil)
	assert.NoError(t, err, "an earlier valid source wins")
	assert.True(t, ok)
	assert.Equal(t, 4, stars)

	_, _, err = pickRating([]string{"FMPS", "WMP"}, found, invalid, nil)
	assert.Error(t, err, "the invalid source comes first: unreadable")

	_, ok, err = pickRating([]string{"MusicBee"}, found, invalid, nil)
	assert.NoError(t, err, "an invalid source outside the order does not matter")
	assert.False(t, ok)
}

func TestPopmWMPToStars(t *testing.T) {
	tests := []struct {
		input byte
		want  int
	}{
		{0, 0}, // unrated
		{1, 1}, // 1 star range [1,1]
		{2, 2},
		{44, 2}, // 2 star range [2,64]
		{64, 2},
		{90, 3}, // 3 star range [65,128]
		{128, 3},
		{196, 4}, // 4 star range [129,196]
		{145, 4},
		{197, 5}, // 5 stars range [197,255]
		{255, 5}, // max byte
	}

	for _, tc := range tests {
		got := popmWMPToStars(tc.input)
		assert.Equal(t, tc.want, got, "input byte=%d", tc.input)
	}
}

func TestPopmITunesToStars(t *testing.T) {
	tests := []struct {
		input byte
		want  int
	}{
		{0, 0}, // unrated
		{1, 1}, // 1 star range (0,20]
		{20, 1},
		{21, 2}, // 2 star range (20,40]
		{40, 2},
		{41, 3}, // 3 star range (40,60]
		{60, 3},
		{61, 4}, // 4 star range (60,80]
		{80, 4},
		{81, 5}, // 5 star range (80,255]
		{100, 5},
		{255, 5},
	}

	for _, tc := range tests {
		got := popmITunesToStars(tc.input)
		assert.Equal(t, tc.want, got, "input byte=%d", tc.input)
	}
}

func TestRatingMusicBeeToStars(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		want   int
	}{
		{"empty string", "", false, 0},
		{"zero", "0", false, 0},
		{"one star", "20", true, 1},
		{"two stars", "40", true, 2},
		{"three stars", "60", true, 3},
		{"four stars", "80", true, 4},
		{"five stars", "100", true, 5},
		{"out of range", "101", false, 0},
		{"non-numeric", "abc", false, 0},
		{"leading zero", "060", true, 3},
		{"plus sign", "+80", true, 4},
		{"half star", "50", false, 0},
		{"foobar2000 1-5 value is not MusicBee's", "4", false, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ratingMusicBeeToStars(tc.input)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestPopmMusicBeeToStars(t *testing.T) {
	for b, want := range map[byte]int{1: 1, 64: 2, 128: 3, 196: 4, 255: 5} {
		assert.Equal(t, want, popmMusicBeeToStars(b), "byte %d", b)
	}
	// Unrated, half stars (13/54/118/186/242) and arbitrary bytes.
	for _, b := range []byte{0, 13, 54, 118, 186, 242, 2, 100, 197, 23, 252} {
		assert.Equal(t, 0, popmMusicBeeToStars(b), "byte %d", b)
	}
}

func TestPopmMediaMonkeyToStars(t *testing.T) {
	for b, want := range map[byte]int{1: 1, 64: 2, 128: 3, 196: 4, 255: 5, 23: 1, 252: 5} {
		assert.Equal(t, want, popmMediaMonkeyToStars(b), "byte %d", b)
	}
	for _, b := range []byte{0, 13, 54, 118, 186, 242, 2, 100, 197} {
		assert.Equal(t, 0, popmMediaMonkeyToStars(b), "byte %d", b)
	}
}

func TestSharedUserRatingToStars(t *testing.T) {
	for v, want := range map[uint32]int{0: 0, 1: 1, 12: 1, 13: 2, 25: 2, 50: 3, 75: 4, 87: 4, 88: 5, 99: 5, 100: 0, 255: 0} {
		assert.Equal(t, want, sharedUserRatingToStars(v), "value %d", v)
	}
}

func TestRatingPercentToStars(t *testing.T) {
	for in, want := range map[string]int{"20": 1, "40": 2, "60": 3, "80": 4, "100": 5, "1": 1, "50": 3, " 81 ": 5} {
		stars, ok, err := ratingPercentToStars(in)
		assert.NoError(t, err, in)
		assert.True(t, ok, in)
		assert.Equal(t, want, stars, in)
	}
	for _, in := range []string{"", "0", "  "} {
		_, ok, err := ratingPercentToStars(in)
		assert.NoError(t, err, in)
		assert.False(t, ok, in)
	}
	// 300 used to wrap to byte 44 (3 stars), 101-255 used to become 5 stars.
	for _, in := range []string{"300", "256", "101", "-20", "60abc", "4.5"} {
		stars, ok, err := ratingPercentToStars(in)
		assert.Error(t, err, in)
		assert.False(t, ok, in)
		assert.Equal(t, 0, stars, in)
	}
}

// An invalid iTunes-style value makes the file unreadable when that source is
// reached; an earlier valid source still wins.
func TestRatingFromM4A_InvalidITunesRating(t *testing.T) {
	data := buildM4A(map[string]string{"rating": "300", "RATE": "80"})

	_, _, err := ratingFromM4A(data, "", []string{"iTunes", "MusicBee"})
	assert.Error(t, err)

	stars, found, err := ratingFromM4A(data, "", []string{"MusicBee", "iTunes"})
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 4, stars)
}
