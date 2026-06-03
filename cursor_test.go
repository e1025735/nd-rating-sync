package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseCursor_EmptyPayloadIsFreshSweep(t *testing.T) {
	cur, resumed := parseCursor("")
	assert.False(t, resumed, "empty payload is a fresh sweep, not a resume")
	assert.Equal(t, syncCursor{}, cur)
}

func TestParseCursor_RoundTrip(t *testing.T) {
	want := syncCursor{Lib: 2, User: 1, Offset: 1500, PairStart: "2026-06-02T18:00:00Z", Single: true}
	cur, resumed := parseCursor(want.marshal())
	assert.True(t, resumed)
	assert.Equal(t, want, cur)
}

func TestParseCursor_MalformedPayloadFallsBackToFreshSweep(t *testing.T) {
	cur, resumed := parseCursor("{not valid json")
	assert.False(t, resumed, "a malformed payload must restart the sweep rather than abort")
	assert.Equal(t, syncCursor{}, cur)
}

// TestSyncCursorMarshal_OmitsSingleWhenFalse keeps the common full-sweep
// continuation payload compact and pins the exact wire form other tests assert.
func TestSyncCursorMarshal_OmitsSingleWhenFalse(t *testing.T) {
	assert.Equal(t, `{"lib":0,"user":0,"off":0,"start":""}`, syncCursor{}.marshal())
	assert.Equal(t,
		`{"lib":1,"user":0,"off":0,"start":"","single":true}`,
		syncCursor{Lib: 1, Single: true}.marshal())
}
