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
