package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFmpsToStars(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		want   int
	}{
		{"empty string", "", false, 0},
		{"zero bare", "0", false, 0},
		{"zero float", "0.0", false, 0},
		{"negative", "-0.5", false, 0},
		{"invalid text", "abc", false, 0},
		{"1 star canonical", "0.2", true, 1},
		{"2 stars canonical", "0.4", true, 2},
		{"3 stars canonical", "0.6", true, 3},
		{"4 stars canonical", "0.8", true, 4},
		{"5 stars canonical", "1.0", true, 5},
		{"tiny positive rounds to 1", "0.01", true, 1},
		{"above 1.0 clamps to 5", "1.5", true, 5},
		{"leading/trailing whitespace", "  0.6  ", true, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := fmpsToStars(tc.input)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestPopmWMPToStars(t *testing.T) {
	tests := []struct {
		input byte
		want  int
	}{
		{0, 0}, // unrated
		{1, 1}, // 1 star range [1,1]
		{2, 2},
		{44, 2}, // 2 star range [2,64]
		{64, 2},
		{90, 3}, // 3 star range [65,128]
		{128, 3},
		{196, 4}, // 4 star range [129,196]
		{145, 4},
		{197, 5}, // 5 stars range [197,255]
		{255, 5}, // max byte
	}

	for _, tc := range tests {
		got := popmWMPToStars(tc.input)
		assert.Equal(t, tc.want, got, "input byte=%d", tc.input)
	}
}

func TestPopmITunesToStars(t *testing.T) {
	tests := []struct {
		input byte
		want  int
	}{
		{0, 0}, // unrated
		{1, 1}, // 1 star range (0,20]
		{20, 1},
		{21, 2}, // 2 star range (20,40]
		{40, 2},
		{41, 3}, // 3 star range (40,60]
		{60, 3},
		{61, 4}, // 4 star range (60,80]
		{80, 4},
		{81, 5}, // 5 star range (80,255]
		{100, 5},
		{255, 5},
	}

	for _, tc := range tests {
		got := popmITunesToStars(tc.input)
		assert.Equal(t, tc.want, got, "input byte=%d", tc.input)
	}
}

func TestRatingMusicBeeToStars(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantOK bool
		want   int
	}{
		{"empty string", "", false, 0},
		{"zero", "0", false, 0},
		{"one star", "20", true, 1},
		{"two stars", "40", true, 2},
		{"three stars", "60", true, 3},
		{"four stars", "80", true, 4},
		{"five stars", "100", true, 5},
		{"out of range", "101", false, 0},
		{"non-numeric", "abc", false, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ratingMusicBeeToStars(tc.input)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
