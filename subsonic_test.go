package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetSubsonicMock(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
	})
}

// subsonicOK builds a valid search3 JSON response body.
func subsonicOK(songs []subsonicSong) string {
	type inner struct {
		Status        string         `json:"status"`
		SearchResult3 *searchResult3 `json:"searchResult3,omitempty"`
	}
	type outer struct {
		Response inner `json:"subsonic-response"`
	}
	body, _ := json.Marshal(outer{
		Response: inner{Status: "ok", SearchResult3: &searchResult3{Song: songs}},
	})
	return string(body)
}

// subsonicErr builds a failed Subsonic response with the given error code.
func subsonicErr(code int, message string) string {
	type errObj struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	type inner struct {
		Status string `json:"status"`
		Error  errObj `json:"error"`
	}
	type outer struct {
		Response inner `json:"subsonic-response"`
	}
	body, _ := json.Marshal(outer{
		Response: inner{Status: "failed", Error: errObj{Code: code, Message: message}},
	})
	return string(body)
}

// ─── fetchAllSongs ────────────────────────────────────────────────────────────

func TestFetchAllSongs_EmptyResult(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice`,
	).Return(subsonicOK(nil), nil)

	songs, err := fetchAllSongs("alice", "")
	require.NoError(t, err)
	assert.Empty(t, songs)
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestFetchAllSongs_SinglePageWithLibraryID(t *testing.T) {
	resetSubsonicMock(t)
	want := []subsonicSong{
		{ID: "1", Title: "Song A", Path: "/music/a.mp3", Suffix: "mp3"},
		{ID: "2", Title: "Song B", Path: "/music/b.mp3", Suffix: "mp3", UserRating: 3},
	}
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice&musicFolderId=lib1`,
	).Return(subsonicOK(want), nil)

	songs, err := fetchAllSongs("alice", "lib1")
	require.NoError(t, err)
	assert.Equal(t, want, songs)
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestFetchAllSongs_MultiPage(t *testing.T) {
	resetSubsonicMock(t)

	fullPage := make([]subsonicSong, 500)
	for i := range fullPage {
		fullPage[i] = subsonicSong{ID: fmt.Sprintf("%d", i+1)}
	}
	partial := []subsonicSong{{ID: "501"}, {ID: "502"}, {ID: "503"}}

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice`,
	).Return(subsonicOK(fullPage), nil).Once()

	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=500&albumCount=0&artistCount=0&u=alice`,
	).Return(subsonicOK(partial), nil).Once()

	songs, err := fetchAllSongs("alice", "")
	require.NoError(t, err)
	assert.Len(t, songs, 503)
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestFetchAllSongs_NetworkError(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice`,
	).Return("", fmt.Errorf("connection refused"))

	_, err := fetchAllSongs("alice", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SubsonicAPICall")
}

func TestFetchAllSongs_SubsonicErrorResponse(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice`,
	).Return(subsonicErr(10, "required parameter missing"), nil)

	_, err := fetchAllSongs("alice", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Subsonic API error 10")
}

func TestFetchAllSongs_MalformedJSON(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call",
		`search3?query=%22%22&songCount=500&songOffset=0&albumCount=0&artistCount=0&u=alice`,
	).Return("{invalid json", nil)

	_, err := fetchAllSongs("alice", "")
	require.Error(t, err)
}

// ─── setRating ────────────────────────────────────────────────────────────────

func TestSetRating_Success(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=4&u=alice").
		Return(`{"subsonic-response":{"status":"ok"}}`, nil)

	err := setRating("alice", "song-1", 4)
	assert.NoError(t, err)
	host.SubsonicAPIMock.AssertExpectations(t)
}

func TestSetRating_NetworkError(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=3&u=alice").
		Return("", fmt.Errorf("timeout"))

	err := setRating("alice", "song-1", 3)
	require.Error(t, err)
}

func TestSetRating_SubsonicReturnsError(t *testing.T) {
	resetSubsonicMock(t)
	host.SubsonicAPIMock.On("Call", "setRating?id=song-1&rating=5&u=alice").
		Return(subsonicErr(50, "user not authorised"), nil)

	err := setRating("alice", "song-1", 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "50")
}
