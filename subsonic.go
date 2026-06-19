package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

type subsonicWrapper struct {
	Response subsonicResponse `json:"subsonic-response"`
}

type subsonicResponse struct {
	Status        string         `json:"status"`
	Error         *subsonicError `json:"error,omitempty"`
	SearchResult3 *searchResult3 `json:"searchResult3,omitempty"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type searchResult3 struct {
	Song []subsonicSong `json:"song"`
}

type subsonicSong struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Path       string `json:"path"`       // Navidrome's reported path — synthesized/fake by default, NOT used to open the file
	Suffix     string `json:"suffix"`     // file extension without the dot, e.g. "mp3"
	Size       int64  `json:"size"`       // file size in bytes — used to locate the real file under the library mount
	UserRating int    `json:"userRating"` // 0 = unrated, 1–5 = stars
}

// songPageSize is the number of songs requested per search3 call. It also
// defines the granularity at which a chunked sync re-fetches when resuming
// from a cursor.
const songPageSize = 500

// fetchSongPage retrieves a single page of songs accessible by username in the
// given library (empty libraryID = all libraries). It returns the page plus a
// "more" flag that is true when the page came back full, i.e. another page may
// follow. A short or empty page reports more=false, which gives the caller a
// well-defined stopping condition even if the server mishandles songOffset
// (preventing an unbounded paging loop).
func fetchSongPage(username, libraryID string, offset, pageSize int) (songs []subsonicSong, more bool, err error) {
	logTrace(fmt.Sprintf("nd-rating-sync: fetchSongPage start username=%q, lib=%q, offset=%q, pageSize=%q", username, libraryID, offset, pageSize))
	uri := fmt.Sprintf(
		"search3?query=%%22%%22&songCount=%d&songOffset=%d&albumCount=0&artistCount=0&u=%s",
		pageSize, offset, url.QueryEscape(username))
	if libraryID != "" {
		uri += "&musicFolderId=" + url.QueryEscape(libraryID)
	}

	logDebug(fmt.Sprintf(
		"nd-rating-sync: fetching songs – user=%q library=%q offset=%d page_size=%d",
		username, libraryID, offset, pageSize))

	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		return nil, false, fmt.Errorf("SubsonicAPICall (offset=%d): %w", offset, err)
	}

	var wrapper subsonicWrapper
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return nil, false, fmt.Errorf("unmarshal search3 response: %w", err)
	}
	if wrapper.Response.Status != "ok" {
		if wrapper.Response.Error != nil {
			return nil, false, fmt.Errorf("Subsonic API error %d: %q",
				wrapper.Response.Error.Code, wrapper.Response.Error.Message)
		}
		return nil, false, errors.New("Subsonic API returned non-ok status")
	}
	if wrapper.Response.SearchResult3 == nil {
		return nil, false, nil
	}
	page := wrapper.Response.SearchResult3.Song
	logTrace(fmt.Sprintf("nd-rating-sync: fetchSongPage done username=%q, lib=%q, offset=%q, pageSize=%q", username, libraryID, offset, pageSize))
	logDebug(fmt.Sprintf(
		"nd-rating-sync: page offset=%d returned %d songs", offset, len(page)))
	return page, len(page) == pageSize, nil
}

// setRating calls the Subsonic setRating endpoint.
func setRating(username, songID string, stars int) error {
	logTrace(fmt.Sprintf("nd-rating-sync: setRating start song=%q, username=%q", songID, username))
	uri := fmt.Sprintf("setRating?id=%s&rating=%d&u=%s", songID, stars, username)
	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: setRating stop, subsonic call error song=%q, username=%q", songID, username))
		return err
	}

	var wrapper subsonicWrapper
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		logTrace(fmt.Sprintf("nd-rating-sync: setRating stop, unmarshal error song=%q, username=%q", songID, username))
		return fmt.Errorf("unmarshal setRating response: %w", err)
	}
	if wrapper.Response.Status != "ok" {
		if wrapper.Response.Error != nil {
			logTrace(fmt.Sprintf("nd-rating-sync: setRating sop, API error song=%q, username=%q", songID, username))
			return fmt.Errorf("API error %d: %s",
				wrapper.Response.Error.Code, wrapper.Response.Error.Message)
		}
		logTrace(fmt.Sprintf("nd-rating-sync: setRating stop non ok status song=%q, username=%q", songID, username))
		return errors.New("setRating returned non-ok status")
	}
	logTrace(fmt.Sprintf("nd-rating-sync: setRating done song=%q, username=%q", songID, username))
	return nil
}
