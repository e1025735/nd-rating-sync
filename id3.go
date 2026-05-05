package main

import (
	"bytes"
	"strings"

	"github.com/bogem/id3v2/v2"
)

// parseID3v2Rating inspects raw MP3 (or any file with an ID3v2 header) data
// and returns a 1–5 star rating by trying each format in tagOrder in sequence.
// The first format that yields a non-zero rating wins.
//
// Recognised tagOrder values:
//   - "MediaMonkey" – TXXX frame with description "FMPS_Rating" (float 0.0–1.0)
//   - "WMP"         – POPM frame written by Windows Media Player
//   - "iTunes"      – POPM frame written by iTunes / Apple Music
func parseID3v2Rating(data []byte, tagOrder []string) (int, bool) {
	tag, err := id3v2.ParseReader(bytes.NewReader(data), id3v2.Options{Parse: true})
	if err != nil {
		return 0, false
	}
	defer tag.Close()

	// Pre-load frames once to avoid repeated scans of the tag.
	popms := tag.GetFrames("POPM")
	txxxs := tag.GetFrames("TXXX")

	for _, format := range tagOrder {
		switch format {
		case "MediaMonkey":
			for _, f := range txxxs {
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
		case "WMP":
			for _, f := range popms {
				popm, ok := f.(id3v2.PopularimeterFrame)
				if !ok {
					continue
				}
				if strings.Contains(strings.ToLower(strings.TrimSpace(popm.Email)), "windows media player") {
					if stars, ok := popmWMPToStars(popm.Rating); ok {
						return stars, true
					}
				}
			}
		case "iTunes":
			for _, f := range popms {
				popm, ok := f.(id3v2.PopularimeterFrame)
				if !ok {
					continue
				}
				e := strings.ToLower(strings.TrimSpace(popm.Email))
				if strings.Contains(e, "itunes") || e == "com.apple.itunes" {
					if stars, ok := popmITunesToStars(popm.Rating); ok {
						return stars, true
					}
				}
			}
		}
	}

	return 0, false
}
