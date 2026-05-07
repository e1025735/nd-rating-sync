package main

import (
	"fmt"
	"math"
	"strings"
)

/*
fmpsToStars reads the FMPS_Rating float (0.0–1.0) and turns it into a 1–5 star value.
Uses ceiling so the canonical values (0.2, 0.4 … 1.0) land exactly on whole stars.
*/
func fmpsToStars(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "0.0" {
		return 0, false
	}

	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, false
	}
	// Reject NaN/Inf explicitly: comparisons with NaN are all false, so
	// `f <= 0` would let it slip through and produce garbage from math.Ceil.
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, false
	}
	if f > 1 {
		f = 1
	}

	// keep the canonical FMPS values (0.2, 0.4 … 1.0) on whole stars
	stars := int(math.Ceil(f * 5))
	if stars > 5 {
		stars = 5
	}
	return stars, true
}

/*
popmWMPToStars decodes a POPM byte written by Windows Media Player.
WMP's internal scale runs 0–99 with fixed star breakpoints (1, 25, 50, 75, 99),
so the byte ranges between those points all collapse to the lower star.
Byte 0 means unrated; anything above 99 is treated as 5 stars.
*/
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

/*
ratingIntToStars parses a plain integer star count in the range 1–5.
Used by foobar2000-style tags (TXXX:RATING in MP3, RATING Vorbis comment
in FLAC / Ogg / Opus) where the value is just the star count as a string.
Empty, "0", or out-of-range values are reported as unrated.
*/
func ratingIntToStars(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, false
	}
	if n < 1 || n > 5 {
		return 0, false
	}
	return n, true
}

/*
popmITunesToStars decodes a POPM byte written by iTunes / Apple Music.
iTunes spreads its 5 stars evenly across 0–100 in steps of 20, so the
mapping is straightforward. Values between steps round up to the next star.
*/
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
