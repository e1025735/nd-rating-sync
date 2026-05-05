package main

import (
	"bytes"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// parseID3v2Rating inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating if it finds either:
//
//   - a TXXX frame whose description is "FMPS_Rating" (float 0.0–1.0), or
//   - a POPM (Popularimeter) frame.
//
// TXXX FMPS_Rating takes precedence over POPM when both are present.
func parseID3v2Rating(data []byte) (int, bool) {
	tag, err := id3v2.ParseReader(bytes.NewReader(data), id3v2.Options{Parse: true})
	if err != nil {
		return 0, false
	}
	defer tag.Close()

	// TXXX FMPS_Rating takes precedence.
	for _, f := range tag.GetFrames("TXXX") {
		txxx, ok := f.(id3v2.UserDefinedTextFrame)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(txxx.Description), "FMPS_Rating") {
			if stars, ok := fmpsToStars(txxx.Value); ok {
				return stars, true
			}
		}
	}

	// Fall back to POPM; pass the email field so popmToStars can select the
	// correct byte→star scale for the application that wrote the frame.
	for _, f := range tag.GetFrames("POPM") {
		popm, ok := f.(id3v2.PopularimeterFrame)
		if !ok {
			continue
		}
		if stars, ok := popmToStars(popm.Email, popm.Rating); ok {
			return stars, true
		}
	}

	return 0, false
}
