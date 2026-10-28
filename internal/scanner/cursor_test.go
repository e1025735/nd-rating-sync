//go:build !tinygo

package scanner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseCursor_EmptyPayloadIsFreshSweep(t *testing.T) {
	cur, resumed := parseCursor("")
	assert.False(t, resumed, "empty payload is a fresh sweep, not a resume")
	assert.Equal(t, SyncCursor{}, cur)
}

func TestParseCursor_RoundTrip(t *testing.T) {
	want := SyncCursor{Lib: 2, User: 1, Offset: 1500, PairStart: "2026-06-02T18:00:00Z", Retry: 2}
	cur, resumed := parseCursor(want.marshal())
	assert.True(t, resumed)
	assert.Equal(t, want, cur)
}

func TestParseCursor_MalformedPayloadFallsBackToFreshSweep(t *testing.T) {
	cur, resumed := parseCursor("{not valid json")
	assert.False(t, resumed, "a malformed payload must restart the sweep rather than abort")
	assert.Equal(t, SyncCursor{}, cur)
}

// TestSyncCursorMarshal pins the exact continuation-payload wire form that
// other tests assert against. Retry is omitted while zero, so payloads of
// older versions and of healthy chains look the same.
func TestSyncCursorMarshal(t *testing.T) {
	assert.Equal(t, `{"lib":0,"user":0,"off":0,"start":""}`, SyncCursor{}.marshal())
	assert.Equal(t,
		`{"lib":1,"user":2,"off":500,"start":"2026-06-02T18:00:00Z"}`,
		SyncCursor{Lib: 1, User: 2, Offset: 500, PairStart: "2026-06-02T18:00:00Z"}.marshal())
	assert.Equal(t,
		`{"lib":0,"user":0,"off":0,"start":"","retry":1}`,
		SyncCursor{Retry: 1}.marshal())
}

func TestSyncCursorNextPair_ClearsPairFields(t *testing.T) {
	cur := SyncCursor{Lib: 1, User: 2, Offset: 7, PairStart: "2026-06-02T18:00:00Z", Retry: 2, LogReadsUntil: 500}
	cur.nextPair()
	assert.Equal(t, SyncCursor{Lib: 1, User: 3}, cur)
}

func TestSyncCursorNextLibrary_ClearsPairFields(t *testing.T) {
	cur := SyncCursor{Lib: 1, User: 2, Offset: 7, PairStart: "2026-06-02T18:00:00Z", Retry: 2, LogReadsUntil: 500}
	cur.nextLibrary()
	assert.Equal(t, SyncCursor{Lib: 2}, cur)
}

// Changing what a sweep writes must change the fingerprint stored with the
// incremental threshold; the order of the tag list matters.
func TestPairFingerprint_ChangesWithPairSettings(t *testing.T) {
	base := UserConfig{Username: "alice", RatingTagOrder: []string{"WMP", "MusicBee"}, SkipAlreadyRated: true}
	fp := pairFingerprint(base)
	assert.NotEmpty(t, fp)

	renamed := base
	renamed.Username = "bob"
	assert.Equal(t, fp, pairFingerprint(renamed), "the username is part of the KV key, not the fingerprint")

	for name, change := range map[string]func(u *UserConfig){
		"tag order": func(u *UserConfig) { u.RatingTagOrder = []string{"MusicBee", "WMP"} },
		"skip":      func(u *UserConfig) { u.SkipAlreadyRated = false },
		"clear":     func(u *UserConfig) { u.ClearRatingIfUntagged = true },
	} {
		u := base
		change(&u)
		assert.NotEqual(t, fp, pairFingerprint(u), name)
	}
}
