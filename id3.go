package main

import (
	"bytes"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// parseID3v2Rating inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating. It first collects every recognised rating
// frame from the file (one pass), then picks the winner by tagOrder.
//
// Recognised tagOrder values:
//   - "MediaMonkey" – TXXX frame with description "FMPS_Rating" (float 0.0–1.0)
//   - "foobar2000"  – TXXX frame with description "RATING" (integer 1–5)
//   - "WMP"         – POPM frame written by Windows Media Player
//   - "iTunes"      – POPM frame written by iTunes / Apple Music
func parseID3v2Rating(data []byte, tagOrder []string) (int, bool) {
	tag, err := id3v2.ParseReader(bytes.NewReader(data), id3v2.Options{Parse: true})
	if err != nil {
		return 0, false
	}
	defer tag.Close()

	found := make(map[string]int) // format → stars

	for _, f := range tag.GetFrames("TXXX") {
		txxx, ok := f.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}
		desc := strings.ToUpper(strings.TrimSpace(txxx.Description))
		switch desc {
		case "FMPS_RATING":
			if stars, ok := fmpsToStars(txxx.Value); ok {
				found["MediaMonkey"] = stars
			}
		case "RATING":
			if stars, ok := ratingIntToStars(txxx.Value); ok {
				found["foobar2000"] = stars
			}
		}
	}

	for _, f := range tag.GetFrames("POPM") {
		popm, ok := f.(id3v2.PopularimeterFrame)
		if !ok {
			continue
		}
		e := strings.ToLower(strings.TrimSpace(popm.Email))
		switch {
		case strings.Contains(e, "windows media player"):
			if stars := popmWMPToStars(popm.Rating); stars > 0 {
				found["WMP"] = stars
			}
		case strings.Contains(e, "itunes") || e == "com.apple.itunes":
			if stars := popmITunesToStars(popm.Rating); stars > 0 {
				found["iTunes"] = stars
			}
		}
	}

	for _, format := range tagOrder {
		if stars, ok := found[format]; ok {
			return stars, true
		}
	}
	return 0, false
}
