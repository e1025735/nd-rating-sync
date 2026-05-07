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
		if ok && stars < 1 {
			t.Errorf("fmpsToStars(%q) = (0, true): ok implies at least 1 star", s)
		}
	})
}

// FuzzPopmConverters drives both POPM byte decoders through arbitrary inputs and
// checks the [0,5] invariant. Cheap belt-and-braces regression guard if the
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
// stays in range. orderIdx selects among representative tagOrder permutations
// (including an empty order) so the fuzzer also explores priority resolution.
func FuzzParseID3v2Rating(f *testing.F) {
	// Seed: valid FMPS + WMP POPM tag.
	tag := id3v2.NewEmptyTag()
	tag.AddFrame("TXXX", id3v2.UserDefinedTextFrame{
		Encoding: id3v2.EncodingUTF8, Description: "FMPS_Rating", Value: "0.6",
	})
	tag.AddFrame("POPM", id3v2.PopularimeterFrame{
		Email: "Windows Media Player 9 Series", Rating: 75, Counter: big.NewInt(0),
	})
	var buf bytes.Buffer
	if _, err := tag.WriteTo(&buf); err == nil {
		f.Add(buf.Bytes(), byte(0))
	}

	// Seed: both WMP and iTunes POPM frames present — exercises tag-order resolution.
	tag2 := id3v2.NewEmptyTag()
	tag2.AddFrame("POPM", id3v2.PopularimeterFrame{
		Email: "Windows Media Player 9 Series", Rating: 75, Counter: big.NewInt(0),
	})
	tag2.AddFrame("POPM", id3v2.PopularimeterFrame{
		Email: "iTunes", Rating: 60, Counter: big.NewInt(0),
	})
	var buf2 bytes.Buffer
	if _, err := tag2.WriteTo(&buf2); err == nil {
		f.Add(buf2.Bytes(), byte(1))
	}

	f.Add([]byte{}, byte(0))
	f.Add([]byte("garbage bytes"), byte(0))
	f.Add([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), byte(0)) // truncated header

	orders := [][]string{
		{"WMP", "iTunes", "MediaMonkey"},
		{"MediaMonkey", "WMP", "iTunes"},
		{"iTunes"},
		{},
	}
	f.Fuzz(func(t *testing.T, data []byte, orderIdx byte) {
		tagOrder := orders[int(orderIdx)%len(orders)]
		stars, ok := parseID3v2Rating(data, tagOrder)
		if stars < 0 || stars > 5 {
			t.Errorf("parseID3v2Rating: stars=%d out of [0,5]", stars)
		}
		if !ok && stars != 0 {
			t.Errorf("parseID3v2Rating: (%d, false) but stars must be 0 when ok is false", stars)
		}
	})
}

func FuzzParseWAVRating(f *testing.F) {
	f.Add([]byte{}, byte(0))
	f.Add([]byte("RIFF\x04\x00\x00\x00WAVE"), byte(0))

	orders := [][]string{
		{"WMP", "iTunes", "MediaMonkey", "foobar2000"},
		{"MediaMonkey", "foobar2000"},
		{},
	}
	f.Fuzz(func(t *testing.T, data []byte, orderIdx byte) {
		tagOrder := orders[int(orderIdx)%len(orders)]
		stars, ok := parseWAVRating(data, tagOrder)
		if stars < 0 || stars > 5 {
			t.Errorf("parseWAVRating: stars=%d out of [0,5]", stars)
		}
		if !ok && stars != 0 {
			t.Errorf("parseWAVRating: (%d, false) but stars must be 0 when ok is false", stars)
		}
	})
}

func FuzzParseDSFRating(f *testing.F) {
	f.Add([]byte{}, byte(0))
	f.Add([]byte("DSD \x1c\x00\x00\x00\x00\x00\x00\x00\x1c\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), byte(0))

	orders := [][]string{
		{"WMP", "iTunes", "MediaMonkey", "foobar2000"},
		{"foobar2000"},
		{},
	}
	f.Fuzz(func(t *testing.T, data []byte, orderIdx byte) {
		tagOrder := orders[int(orderIdx)%len(orders)]
		stars, ok := parseDSFRating(data, tagOrder)
		if stars < 0 || stars > 5 {
			t.Errorf("parseDSFRating: stars=%d out of [0,5]", stars)
		}
		if !ok && stars != 0 {
			t.Errorf("parseDSFRating: (%d, false) but stars must be 0 when ok is false", stars)
		}
	})
}

func FuzzParseM4ARating(f *testing.F) {
	f.Add([]byte{}, byte(0))
	f.Add(buildM4A(map[string]string{"FMPS_Rating": "0.6"}), byte(0))
	f.Add(buildM4A(map[string]string{"RATING": "3", "rating": "60"}), byte(1))

	orders := [][]string{
		{"WMP", "iTunes", "MediaMonkey", "foobar2000"},
		{"iTunes", "foobar2000"},
		{},
	}
	f.Fuzz(func(t *testing.T, data []byte, orderIdx byte) {
		tagOrder := orders[int(orderIdx)%len(orders)]
		stars, ok := parseM4ARating(data, tagOrder)
		if stars < 0 || stars > 5 {
			t.Errorf("parseM4ARating: stars=%d out of [0,5]", stars)
		}
		if !ok && stars != 0 {
			t.Errorf("parseM4ARating: (%d, false) but stars must be 0 when ok is false", stars)
		}
	})
}

func FuzzParseWMARating(f *testing.F) {
	f.Add([]byte{}, byte(0))
	f.Add(asfHeaderObjectGUID, byte(0))

	orders := [][]string{
		{"WMP", "MediaMonkey"},
		{"MediaMonkey"},
		{},
	}
	f.Fuzz(func(t *testing.T, data []byte, orderIdx byte) {
		tagOrder := orders[int(orderIdx)%len(orders)]
		stars, ok := parseWMARating(data, tagOrder)
		if stars < 0 || stars > 5 {
			t.Errorf("parseWMARating: stars=%d out of [0,5]", stars)
		}
		if !ok && stars != 0 {
			t.Errorf("parseWMARating: (%d, false) but stars must be 0 when ok is false", stars)
		}
	})
}