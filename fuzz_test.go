package main

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/bogem/id3v2/v2"
)

// FuzzFmpsToStars feeds arbitrary strings into the FMPS parser and asserts
// that it never panics and that the returned star count is always in [0,5],
// and that a (false) result implies stars==0.
func FuzzFmpsToStars(f *testing.F) {
	for _, seed := range []string{"", "0", "0.0", "0.2", "0.6", "1.0", "1.5", "abc", "-1", "1e10", "NaN", "  0.6  "} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		stars, ok := fmpsToStars(s)
		if stars < 0 || stars > 5 {
			t.Errorf("fmpsToStars(%q) = %d (out of [0,5])", s, stars)
		}
		if !ok && stars != 0 {
			t.Errorf("fmpsToStars(%q) = (%d, false) but stars must be 0 when ok is false", s, stars)
		}
	})
}

// FuzzPopmConverters drives both POPM byte decoders through every input and
// checks the [0,5] invariant. Cheap belt-and-braces — exhaustive over the
// 256-value input space, but runs as a future regression guard if the
// signatures ever change.
func FuzzPopmConverters(f *testing.F) {
	for _, seed := range []byte{0, 1, 24, 25, 49, 50, 74, 75, 80, 99, 100, 255} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b byte) {
		if s := popmWMPToStars(b); s < 0 || s > 5 {
			t.Errorf("popmWMPToStars(%d) = %d (out of [0,5])", b, s)
		}
		if s := popmITunesToStars(b); s < 0 || s > 5 {
			t.Errorf("popmITunesToStars(%d) = %d (out of [0,5])", b, s)
		}
	})
}

// FuzzParseID3v2Rating feeds arbitrary byte slices into the ID3 parser to
// verify that no malformed input can panic, and that the returned star count
// stays in range.
func FuzzParseID3v2Rating(f *testing.F) {
	// Seed with a valid FMPS tag so the fuzzer has a structured starting point.
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: "0.6",
	})
	tag.AddFrame("POPM", id3v2.PopularimeterFrame{
		Email: "Windows Media Player 9 Series", Rating: 75, Counter: big.NewInt(0),
	})
	var buf bytes.Buffer
	if _, err := tag.WriteTo(&buf); err == nil {
		f.Add(buf.Bytes())
	}
	f.Add([]byte{})
	f.Add([]byte("garbage bytes"))
	f.Add([]byte("ID3\x04\x00\x00\x00\x00\x00\x00")) // truncated header

	tagOrder := []string{"WMP", "iTunes", "MediaMonkey"}
	f.Fuzz(func(t *testing.T, data []byte) {
		stars, ok := parseID3v2Rating(data, tagOrder)
		if stars < 0 || stars > 5 {
			t.Errorf("parseID3v2Rating: stars=%d out of [0,5]", stars)
		}
		if !ok && stars != 0 {
			t.Errorf("parseID3v2Rating: (%d, false) but stars must be 0 when ok is false", stars)
		}
	})
}