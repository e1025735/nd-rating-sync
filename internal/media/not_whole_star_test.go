//go:build !tinygo

package media

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A file read to a clean end without a rating is rated 0 stars and may be
// cleared. A file whose rating is a half star or out of range is rated: it
// must report ErrNotWholeStar, never "no rating", or clear_rating_if_untagged
// would set a 3½-star song to 0.
func TestNotWholeStar_EveryFormat(t *testing.T) {
	cases := []struct {
		name  string
		parse func() (int, bool, error)
	}{
		{"MusicBee POPM half star", func() (int, bool, error) {
			return ratingFromID3v2(makeTagWithPOPM(t, "MusicBee", 186), "", []string{"MusicBee"})
		}},
		{"MediaMonkey POPM unknown byte", func() (int, bool, error) {
			return ratingFromID3v2(makeTagWithPOPM(t, "no@email", 100), "", []string{"MediaMonkey"})
		}},
		{"ID3 FMPS half star", func() (int, bool, error) {
			return ratingFromID3v2(realID3v2(t, "0.7"), "", []string{"FMPS"})
		}},
		{"Vorbis RATING half star", func() (int, bool, error) {
			return ratingFromFLAC(makeFLAC(t, "RATING=70"), "", []string{"MusicBee"})
		}},
		{"Vorbis RATING on foobar2000's scale for MusicBee", func() (int, bool, error) {
			return ratingFromFLAC(makeFLAC(t, "RATING=3"), "", []string{"MusicBee"})
		}},
		{"Vorbis RATING out of range for foobar2000", func() (int, bool, error) {
			return ratingFromFLAC(makeFLAC(t, "RATING=7"), "", []string{"foobar2000"})
		}},
		{"MP4 RATE half star for MediaMonkey", func() (int, bool, error) {
			return ratingFromM4A(buildM4A(map[string]string{"RATE": "50"}), "", []string{"MediaMonkey"})
		}},
		{"WMA SharedUserRating out of range", func() (int, bool, error) {
			return ratingFromWMA(buildWMA(t, []wmaDescriptor{{"WM/SharedUserRating", asfTypeDWORD, wmaDWordValue(150)}}), "", []string{"WMP"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stars, found, err := tc.parse()
			assert.ErrorIs(t, err, ErrNotWholeStar)
			assert.False(t, found)
			assert.Zero(t, stars)
		})
	}
}

// An empty or 0 value holds no rating: the file is rated 0 stars.
func TestNotWholeStar_ZeroIsNoRating(t *testing.T) {
	cases := []struct {
		name  string
		parse func() (int, bool, error)
	}{
		{"MusicBee POPM 0", func() (int, bool, error) {
			return ratingFromID3v2(makeTagWithPOPM(t, "MusicBee", 0), "", []string{"MusicBee"})
		}},
		{"ID3 FMPS 0.0", func() (int, bool, error) {
			return ratingFromID3v2(realID3v2(t, "0.0"), "", []string{"FMPS"})
		}},
		{"Vorbis RATING 0", func() (int, bool, error) {
			return ratingFromFLAC(makeFLAC(t, "RATING=0"), "", []string{"foobar2000", "MusicBee"})
		}},
		{"WMA SharedUserRating 0", func() (int, bool, error) {
			return ratingFromWMA(buildWMA(t, []wmaDescriptor{{"WM/SharedUserRating", asfTypeDWORD, wmaDWordValue(0)}}), "", []string{"WMP"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stars, found, err := tc.parse()
			require.NoError(t, err)
			assert.False(t, found)
			assert.Zero(t, stars)
		})
	}
}

// A whole-star rating from a later source still wins over a half star.
func TestNotWholeStar_LaterWholeStarWins(t *testing.T) {
	stars, found, err := ratingFromFLAC(makeFLAC(t, "RATING=3"), "", []string{"MusicBee", "foobar2000"})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, 3, stars)
}

// Only sources in tagOrder count: a half star from a source the user did
// not list leaves the file without a rating.
func TestNotWholeStar_UnlistedSourceIgnored(t *testing.T) {
	_, found, err := ratingFromID3v2(makeTagWithPOPM(t, "MusicBee", 186), "", []string{"FMPS"})
	require.NoError(t, err)
	assert.False(t, found)
}

func TestPickRating_NotWholeStar(t *testing.T) {
	unusable := map[string]bool{"MusicBee": true}
	_, _, err := pickRating([]string{"MusicBee"}, nil, nil, unusable)
	assert.ErrorIs(t, err, ErrNotWholeStar)

	stars, ok, err := pickRating([]string{"MusicBee", "WMP"}, map[string]int{"WMP": 2}, nil, unusable)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 2, stars)
}
