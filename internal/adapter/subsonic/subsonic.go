package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
)

// SongPageSize is the number of songs requested per search3 call. It also
// defines the granularity at which a chunked sync re-fetches when resuming
// from a cursor.
const SongPageSize = 500

type SubsonicWrapper struct {
	Response SubsonicResponse `json:"subsonic-response"`
}

type SubsonicResponse struct {
	Status        string         `json:"status"`
	Error         *SubsonicError `json:"error,omitempty"`
	SearchResult3 *SearchResult3 `json:"searchResult3,omitempty"`
}

type SubsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type SearchResult3 struct {
	Song []SubsonicSong `json:"song"`
}

type SubsonicSong struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Path       string `json:"path"`       // Navidrome's reported path — synthesized/fake by default, NOT used to open the file
	Suffix     string `json:"suffix"`     // file extension without the dot, e.g. "mp3"
	Size       int64  `json:"size"`       // file size in bytes — used to locate the real file under the library mount
	UserRating int    `json:"userRating"` // 0 = unrated, 1–5 = stars
}

// FetchSongPage retrieves a single page of songs accessible by username in the
// given library (empty libraryID = all libraries). It returns the page plus a
// "more" flag that is true when the page came back full, i.e. another page may
// follow. A short or empty page reports more=false, which gives the caller a
// well-defined stopping condition even if the server mishandles songOffset
// (preventing an unbounded paging loop).
func FetchSongPage(username, libraryID string, offset, pageSize int) (songs []SubsonicSong, more bool, err error) {
	subsonicLogTrace(fmt.Sprintf("nd-rating-sync: fetchSongPage start username=%q, lib=%q, offset=%q, pageSize=%q", username, libraryID, offset, pageSize))
	uri := fmt.Sprintf(
		"search3?query=%%22%%22&songCount=%d&songOffset=%d&albumCount=0&artistCount=0&u=%s",
		pageSize, offset, url.QueryEscape(username))
	if libraryID != "" {
		uri += "&musicFolderId=" + url.QueryEscape(libraryID)
	}

	subsonicLogDebug(fmt.Sprintf(
		"nd-rating-sync: fetching songs – user=%q library=%q offset=%d page_size=%d",
		username, libraryID, offset, pageSize))

	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		return nil, false, fmt.Errorf("SubsonicAPICall (offset=%d): %w", offset, err)
	}

	var wrapper SubsonicWrapper
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
	subsonicLogTrace(fmt.Sprintf("nd-rating-sync: fetchSongPage done username=%q, lib=%q, offset=%q, pageSize=%q", username, libraryID, offset, pageSize))
	subsonicLogDebug(fmt.Sprintf(
		"nd-rating-sync: page offset=%d returned %d songs", offset, len(page)))
	return page, len(page) == pageSize, nil
}

// SetRating calls the Subsonic setRating endpoint.
func SetRating(username, songID string, stars int) error {
	subsonicLogTrace(fmt.Sprintf("nd-rating-sync: setRating start song=%q, username=%q", songID, username))
	uri := fmt.Sprintf("setRating?id=%s&rating=%d&u=%s", songID, stars, username)
	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		subsonicLogTrace(fmt.Sprintf("nd-rating-sync: setRating stop, subsonic call error song=%q, username=%q", songID, username))
		return err
	}

	var wrapper SubsonicWrapper
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		subsonicLogTrace(fmt.Sprintf("nd-rating-sync: setRating stop, unmarshal error song=%q, username=%q", songID, username))
		return fmt.Errorf("unmarshal setRating response: %w", err)
	}
	if wrapper.Response.Status != "ok" {
		if wrapper.Response.Error != nil {
			subsonicLogTrace(fmt.Sprintf("nd-rating-sync: setRating sop, API error song=%q, username=%q", songID, username))
			return fmt.Errorf("API error %d: %s",
				wrapper.Response.Error.Code, wrapper.Response.Error.Message)
		}
		subsonicLogTrace(fmt.Sprintf("nd-rating-sync: setRating stop non ok status song=%q, username=%q", songID, username))
		return errors.New("setRating returned non-ok status")
	}
	subsonicLogTrace(fmt.Sprintf("nd-rating-sync: setRating done song=%q, username=%q", songID, username))
	return nil
}

func subsonicLogInfo(string)  {}
func subsonicLogWarn(string)  {}
func subsonicLogDebug(string) {}
func subsonicLogTrace(string) {}
