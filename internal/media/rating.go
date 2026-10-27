package media

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/e1025735/nd-rating-sync/internal/logging"
)

const (
	wmp1Star       = 1
	wmp2Star       = 64
	wmp3Star       = 128
	wmp4Star       = 196
	iTunes1StarMax = 20
	iTunes2StarMax = 40
	iTunes3StarMax = 60
	iTunes4StarMax = 80
	musicBee1Star  = 20
	musicBee2Star  = 40
	musicBee3Star  = 60
	musicBee4Star  = 80
	musicBee5Star  = 100

	// fmpsWholeStarTolerance absorbs float noise in values like "0.6" or
	// "0.60000002" while still rejecting half stars (0.1 steps = 0.5 star).
	fmpsWholeStarTolerance = 0.05
)

/*
popmWholeStarValues maps the exact POPM bytes MusicBee and MediaMonkey write
for whole stars. Both players share one table, and it is NOT monotonic with
the half stars they also write (½★=13, 1½★=54, 2½★=118, 3½★=186, 4½★=242):
½★ is a larger byte than 1★. Range-based decoding therefore cannot work for
these players; only exact values are accepted, and half stars (Navidrome has
none) or anything else is reported as unrated.
*/
var popmWholeStarValues = map[byte]int{1: 1, 64: 2, 128: 3, 196: 4, 255: 5}

// popmMediaMonkeyLegacyValues are whole-star bytes written by MediaMonkey 3.x
// (1★=23, 5★=252) before it switched to the shared table above.
var popmMediaMonkeyLegacyValues = map[byte]int{23: 1, 252: 5}

// popmMusicBeeToStars decodes a POPM byte written by MusicBee ("MusicBee").
func popmMusicBeeToStars(b byte) int {
	return popmWholeStarValues[b]
}

// popmMediaMonkeyToStars decodes a POPM byte written by MediaMonkey
// ("no@email"), including its legacy 3.x whole-star values.
func popmMediaMonkeyToStars(b byte) int {
	if stars, ok := popmWholeStarValues[b]; ok {
		return stars
	}
	return popmMediaMonkeyLegacyValues[b]
}

/*
fmpsToStars reads the FMPS_Rating float (0.0–1.0) and turns it into a 1–5 star value.
Only whole stars (0.2, 0.4 … 1.0) are accepted: Navidrome has no half stars, so
half-star values such as 0.1 or 0.9 are reported as unrated (ok=false, no
error) instead of being rounded to a neighbouring star. An empty value or 0 is
unrated too.

A value that is not a number (the whole trimmed string must parse) or lies
outside 0.0–1.0 — e.g. a tool that wrote stars ("4") or percent ("80") into
the field — is an error: the caller reports the file as unreadable, so
nothing is written and nothing is cleared.
*/
func fmpsToStars(s string) (int, bool, error) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: fmpsToStars start s=%q", s))
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false, fmt.Errorf("FMPS_Rating %q is not a number", s)
	}
	// NaN fails every comparison, so it is rejected explicitly.
	if math.IsNaN(f) || f < 0 || f > 1 {
		return 0, false, fmt.Errorf("FMPS_Rating %q is outside 0.0–1.0", s)
	}
	if f == 0 {
		return 0, false, nil
	}

	scaled := f * 5
	stars := int(math.Round(scaled))
	if stars < 1 || math.Abs(scaled-float64(stars)) > fmpsWholeStarTolerance {
		logging.Trace(fmt.Sprintf("nd-rating-sync: fmpsToStars stop, not a whole star s=%q", s))
		return 0, false, nil
	}
	logging.Trace(fmt.Sprintf("nd-rating-sync: fmpsToStars done s=%q", s))
	return stars, true, nil
}

// ratings collects the rating every source holds in one file; pick then
// chooses by tagOrder. Each format's parser fills it while it walks the tags.
type ratings struct {
	path    string           // for trace lines only
	found   map[string]int   // source → stars
	invalid map[string]error // source → error of a value that is present but invalid
}

func newRatings(path string) *ratings {
	return &ratings{path: path, found: map[string]int{}, invalid: map[string]error{}}
}

// set records stars for each of sources. 0 (unrated) records nothing.
func (r *ratings) set(stars int, sources ...string) {
	if stars <= 0 {
		return
	}
	for _, s := range sources {
		r.found[s] = stars
	}
	logging.Trace(fmt.Sprintf("nd-rating-sync: found %d stars for %s in %q", stars, strings.Join(sources, " and "), r.path))
}

// parsed records the result of a converter that rejects invalid values: an
// error marks sources invalid, otherwise the stars are set when ok.
func (r *ratings) parsed(stars int, ok bool, err error, sources ...string) {
	if err != nil {
		for _, s := range sources {
			r.invalid[s] = err
		}
		return
	}
	if ok {
		r.set(stars, sources...)
	}
}

// fmps records an FMPS_Rating value. FMPS reads it, and so does MediaMonkey
// as the fallback behind its own field.
func (r *ratings) fmps(value string) {
	stars, ok, err := fmpsToStars(value)
	r.parsed(stars, ok, err, "FMPS", "MediaMonkey")
}

// has reports whether a rating was recorded for source.
func (r *ratings) has(source string) bool {
	_, ok := r.found[source]
	return ok
}

// pick returns the rating of the first source in tagOrder (see pickRating).
func (r *ratings) pick(tagOrder []string) (int, bool, error) {
	return pickRating(tagOrder, r.found, r.invalid)
}

// pickRating returns the rating of the first source in tagOrder that the
// file holds. found maps a source to its stars; invalid maps a source to the
// error of a value that is present but invalid. Reaching an invalid source
// before any valid one is an error, so the file is unreadable rather than
// taking a rating from a later source or counting as untagged.
func pickRating(tagOrder []string, found map[string]int, invalid map[string]error) (int, bool, error) {
	for _, format := range tagOrder {
		if stars, ok := found[format]; ok {
			return stars, true, nil
		}
		if err := invalid[format]; err != nil {
			return 0, false, err
		}
	}
	return 0, false, nil
}

/*
popmWMPToStars decodes a POPM byte written by Windows Media Player.
WMP's internal scale runs 0–255 with fixed star breakpoints (1, 64, 128, 196, 255),
so the byte ranges between those points all collapse to the lower star.
Byte 0 means unrated; anything above 197 is treated as 5 stars.
*/
func popmWMPToStars(b byte) int {
	switch {
	case b == 0:
		return 0 // unrated
	case b <= wmp1Star:
		return 1
	case b <= wmp2Star:
		return 2
	case b <= wmp3Star:
		return 3
	case b <= wmp4Star:
		return 4
	default:
		return 5
	}
}

/*
sharedUserRatingToStars decodes the WMA WM/SharedUserRating value. Windows
uses a 0–99 scale (WMP writes 1/25/50/75/99) and groups the values into
stars with the same ranges as the Windows System.Rating property.
0 means unrated; values above 99 are out of range and reported as unrated.
*/
func sharedUserRatingToStars(v uint32) int {
	switch {
	case v == 0 || v > 99:
		return 0
	case v <= 12:
		return 1
	case v <= 37:
		return 2
	case v <= 62:
		return 3
	case v <= 87:
		return 4
	default:
		return 5
	}
}

/*
ratingIntToStars parses a plain integer star count in the range 1–5.
Used by foobar2000-style tags (TXXX:RATING in MP3, RATING Vorbis comment
in FLAC / Ogg / Opus) where the value is just the star count as a string.
Empty, "0", or out-of-range values are reported as unrated.
*/
func ratingIntToStars(s string) (int, bool) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: ratingIntToStars start s=%q", s))
	s = strings.TrimSpace(s)
	if s == "" {
		logging.Trace(fmt.Sprintf("nd-rating-sync: ratingIntToStars stop, zero s=%q", s))
		return 0, false
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		logging.Trace(fmt.Sprintf("nd-rating-sync: ratingIntToStars stop, error s=%q", s))
		return 0, false
	}

	if n < 1 || n > 5 {
		logging.Trace(fmt.Sprintf("nd-rating-sync: ratingIntToStars stop, out of bounds s=%q", s))
		return 0, false
	}

	logging.Trace(fmt.Sprintf("nd-rating-sync: ratingIntToStars done s=%q", s))
	return n, true
}

/*
ratingMusicBeeToStars parses a 0–100 rating in steps of 20 into 1–5 stars.
MusicBee and MediaMonkey both write this scale: RATING Vorbis comment in
FLAC / Ogg / Opus, and the RATE / rate freeform atom in M4A.
Empty, half-star (10, 30 … 90) or out-of-range values are reported as unrated.
*/
func ratingMusicBeeToStars(s string) (int, bool) {
	logging.Trace(fmt.Sprintf("nd-rating-sync: ratingMusicBeeToStars start s=%q", s))
	s = strings.TrimSpace(s)
	if s == "" {
		logging.Trace(fmt.Sprintf("nd-rating-sync: ratingMusicBeeToStars stop, zero s=%q", s))
		return 0, false
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		logging.Trace(fmt.Sprintf("nd-rating-sync: ratingMusicBeeToStars stop, error s=%q", s))
		return 0, false
	}

	if n < 0 || n > musicBee5Star {
		logging.Trace(fmt.Sprintf("nd-rating-sync: ratingMusicBeeToStars stop, out of bounds s=%q", s))
		return 0, false
	}

	logging.Trace(fmt.Sprintf("nd-rating-sync: ratingMusicBeeToStars done s=%q", s))
	// Compared as a number, so "060" or "+60" read like "60".
	switch n {
	case musicBee1Star:
		return 1, true
	case musicBee2Star:
		return 2, true
	case musicBee3Star:
		return 3, true
	case musicBee4Star:
		return 4, true
	case musicBee5Star:
		return 5, true
	default:
		return 0, false
	}
}

/*
ratingPercentToStars reads the 0–100 value of the iTunes-style MP4 "rating"
atom with the same ranges as popmITunesToStars (1–20 → 1 star … 81–100 → 5
stars); empty or 0 is unrated. A value that is not a whole number or lies
outside 0–100 is an error, so a broken value never becomes a rating.
*/
func ratingPercentToStars(s string) (int, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false, fmt.Errorf("rating %q is not a whole number", s)
	}
	if n < 0 || n > 100 {
		return 0, false, fmt.Errorf("rating %q is outside 0–100", s)
	}
	if n == 0 {
		return 0, false, nil
	}
	return popmITunesToStars(byte(n)), true, nil
}

/*
popmITunesToStars decodes a POPM byte written by iTunes / Apple Music.
iTunes spreads its 5 stars evenly across 0–100 in steps of 20, so the
mapping is straightforward. Values between steps round up to the next star.
*/
func popmITunesToStars(b byte) int {
	switch {
	case b == 0:
		return 0 // unrated
	case b <= iTunes1StarMax:
		return 1
	case b <= iTunes2StarMax:
		return 2
	case b <= iTunes3StarMax:
		return 3
	case b <= iTunes4StarMax:
		return 4
	default:
		return 5
	}
}
