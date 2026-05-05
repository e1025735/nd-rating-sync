package main

import (
	"fmt"
	"math"
	"strings"
)

// fmpsToStars converts an FMPS_Rating float string (0.0–1.0) to 1–5 stars.
// Returns (0, false) if the string cannot be parsed or is zero.
//
// Standard FMPS values map to stars via ceiling:
//
//	0.2 → 1 star, 0.4 → 2, 0.6 → 3, 0.8 → 4, 1.0 → 5
func fmpsToStars(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "0.0" {
		return 0, false
	}

	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, false
	}
	if f <= 0 {
		return 0, false
	}
	if f > 1 {
		f = 1
	}

	// Ceiling maps each fifth of the range to one star level so that the
	// canonical FMPS values (0.2, 0.4, 0.6, 0.8, 1.0) round correctly.
	stars := int(math.Ceil(f * 5))
	if stars > 5 {
		stars = 5
	}
	return stars, true
}

// popmToStars converts a POPM (Popularimeter) byte to 1–5 stars.
//
// The email field is the tagger identifier written by the application that set
// the rating.  Different applications use fundamentally different byte scales,
// so the email must be matched before interpreting the byte value:
//
//   - Windows Media Player: non-linear fixed points (1/25/50/75/99)
//   - iTunes / Apple Music:  linear 0-100 scale    (20/40/60/80/100)
//   - Winamp / MediaMonkey:  percentile bands       (1-63/64-127/128-191/192-223/224-255)
//   - MusicBee / Banshee / Rhythmbox / Clementine:
//     linear 51-unit steps   (51/102/153/204/255)
//
// An empty or unrecognised email falls back to Winamp bands — the most
// widely-deployed POPM convention for generic taggers.
func popmToStars(email string, b byte) (int, bool) {
	if b == 0 {
		return 0, false
	}

	e := strings.ToLower(strings.TrimSpace(email))

	switch {
	case strings.Contains(e, "windows media player"):
		return popmWMPToStars(b)

	case strings.Contains(e, "itunes") || e == "com.apple.itunes":
		return popmITunesToStars(b)

	case e == "no@email" ||
		strings.Contains(e, "winamp") ||
		e == "mm@mm.mm" ||
		strings.Contains(e, "mediamonkey"):
		return popmWinampToStars(b)

	case e == "musicbee" ||
		strings.Contains(e, "banshee") ||
		strings.Contains(e, "rhythmbox") ||
		strings.Contains(e, "clementine"):
		return popmLinear51ToStars(b)

	default:
		// Unknown tagger — Winamp percentile bands are the most widely deployed
		// convention for generic POPM frames.
		return popmWinampToStars(b)
	}
}

// popmWMPToStars applies the Windows Media Player POPM scale.
//
// WMP uses non-linear fixed points chosen to match its internal 0–99 rating:
// 0=unrated, 1=★, 25=★★, 50=★★★, 75=★★★★, 99=★★★★★.
// Values in between are interpolated toward the nearest star.
func popmWMPToStars(b byte) (int, bool) {
	switch {
	case b == 0:
		return 0, false
	case b < 25:
		return 1, true
	case b < 50:
		return 2, true
	case b < 75:
		return 3, true
	case b < 99:
		return 4, true
	default: // 99–255
		return 5, true
	}
}

// popmITunesToStars applies the iTunes / Apple Music POPM scale.
//
// iTunes maps its 5-star system onto the 0–100 byte range:
// 0=unrated, 20=★, 40=★★, 60=★★★, 80=★★★★, 100=★★★★★.
// Intermediate values are rounded toward the nearest star boundary.
func popmITunesToStars(b byte) (int, bool) {
	switch {
	case b == 0:
		return 0, false
	case b <= 20:
		return 1, true
	case b <= 40:
		return 2, true
	case b <= 60:
		return 3, true
	case b <= 80:
		return 4, true
	default: // 81–255
		return 5, true
	}
}

// popmWinampToStars applies the Winamp / MediaMonkey percentile band scale.
//
// The 0–255 range is divided into five unequal bands (a Winamp convention that
// became the de-facto default for generic POPM frames):
// 1-63=★, 64-127=★★, 128-191=★★★, 192-223=★★★★, 224-255=★★★★★.
func popmWinampToStars(b byte) (int, bool) {
	switch {
	case b == 0:
		return 0, false
	case b < 64:
		return 1, true
	case b < 128:
		return 2, true
	case b < 192:
		return 3, true
	case b < 224:
		return 4, true
	default: // 224–255
		return 5, true
	}
}

// popmLinear51ToStars applies the MusicBee / Banshee / Rhythmbox / Clementine
// linear 51-unit step scale.
//
// These apps divide the 0–255 range into equal steps of 51:
// 0=unrated, 51=★, 102=★★, 153=★★★, 204=★★★★, 255=★★★★★.
// Half-star values (e.g. MusicBee 3.5★ = byte 179) are rounded up to the next
// whole star, matching the nearest boundary.
func popmLinear51ToStars(b byte) (int, bool) {
	switch {
	case b == 0:
		return 0, false
	case b <= 51:
		return 1, true
	case b <= 102:
		return 2, true
	case b <= 153:
		return 3, true
	case b <= 204:
		return 4, true
	default: // 205–255
		return 5, true
	}
}
