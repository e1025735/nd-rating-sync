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

// popmWMPToStars applies the Windows Media Player POPM scale.
//
// WMP uses non-linear fixed points chosen to match its internal 0–99 rating:
// 0=unrated, 1=★, 25=★★, 50=★★★, 75=★★★★, 99=★★★★★.
// Values in between are interpolated toward the nearest star.
// Returns 0 for unrated (byte 0); no parsing is involved so no bool is needed.
func popmWMPToStars(b byte) int {
	switch {
	case b == 0:
		return 0
	case b < 25:
		return 1
	case b < 50:
		return 2
	case b < 75:
		return 3
	case b < 99:
		return 4
	default: // 99–255
		return 5
	}
}

// popmITunesToStars applies the iTunes / Apple Music POPM scale.
//
// iTunes maps its 5-star system onto the 0–100 byte range:
// 0=unrated, 20=★, 40=★★, 60=★★★, 80=★★★★, 100=★★★★★.
// Intermediate values are rounded toward the nearest star boundary.
// Returns 0 for unrated (byte 0); no parsing is involved so no bool is needed.
func popmITunesToStars(b byte) int {
	switch {
	case b == 0:
		return 0
	case b <= 20:
		return 1
	case b <= 40:
		return 2
	case b <= 60:
		return 3
	case b <= 80:
		return 4
	default: // 81–255
		return 5
	}
}
