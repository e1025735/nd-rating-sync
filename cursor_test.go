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
	want := syncCursor{Lib: 2, User: 1, Offset: 1500, PairStart: "2026-06-02T18:00:00Z"}
	cur, resumed := parseCursor(want.marshal())
	assert.True(t, resumed)
	assert.Equal(t, want, cur)
}

func TestParseCursor_MalformedPayloadFallsBackToFreshSweep(t *testing.T) {
	cur, resumed := parseCursor("{not valid json")
	assert.False(t, resumed, "a malformed payload must restart the sweep rather than abort")
	assert.Equal(t, syncCursor{}, cur)
}

// TestSyncCursorMarshal pins the exact continuation-payload wire form that
// other tests assert against.
func TestSyncCursorMarshal(t *testing.T) {
	assert.Equal(t, `{"lib":0,"user":0,"off":0,"start":""}`, syncCursor{}.marshal())
	assert.Equal(t,
		`{"lib":1,"user":2,"off":500,"start":"2026-06-02T18:00:00Z"}`,
		syncCursor{Lib: 1, User: 2, Offset: 500, PairStart: "2026-06-02T18:00:00Z"}.marshal())
}
