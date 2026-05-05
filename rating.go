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

// popmToStars converts a POPM byte to 1–5 stars using the scale appropriate
// for the application identified by the email field.
//
// Different tag editors write POPM ratings on different byte scales:
//
//   - Windows Media Player ("Windows Media Player 9 Series"):
//     non-linear fixed points — 0 unrated, 1=★, 25=★★, 50=★★★, 75=★★★★, 99+=★★★★★
//
//   - Winamp, MediaMonkey, MusicBee, foobar2000, and most other editors:
//     percentile bands — 1-63=★, 64-127=★★, 128-191=★★★, 192-223=★★★★, 224-255=★★★★★
func popmToStars(email string, b byte) (int, bool) {
	if b == 0 {
		return 0, false
	}
	if strings.Contains(strings.ToLower(email), "windows media player") {
		return popmWMPToStars(b)
	}
	return popmWinampToStars(b)
}

// popmWMPToStars applies the Windows Media Player POPM scale.
// WMP fixed points: 0 unrated, 1=★, 25=★★, 50=★★★, 75=★★★★, 99+=★★★★★.
func popmWMPToStars(b byte) (int, bool) {
	switch {
	case b < 1:
		return 0, false
	case b < 25:
		return 1, true
	case b < 50:
		return 2, true
	case b < 75:
		return 3, true
	case b < 99:
		return 4, true
	default:
		return 5, true
	}
}

// popmWinampToStars applies the Winamp/MediaMonkey percentile band scale,
// which is the de-facto standard for all tag editors except Windows Media Player.
// 1-63=★, 64-127=★★, 128-191=★★★, 192-223=★★★★, 224-255=★★★★★.
func popmWinampToStars(b byte) (int, bool) {
	switch {
	case b < 64:
		return 1, true
	case b < 128:
		return 2, true
	case b < 192:
		return 3, true
	case b < 224:
		return 4, true
	default:
		return 5, true
	}
}
