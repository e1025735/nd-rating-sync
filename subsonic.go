package main

import (
	"encoding/json"
	"errors"
	"fmt"

	pdk "github.com/extism/go-pdk"
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
	Path       string `json:"path"`
	Suffix     string `json:"suffix"`
	UserRating int    `json:"userRating"` // 0 = unrated, 1–5 = stars
}

// fetchAllSongs pages through search3 and returns every song accessible by
// username in the given library. Pass an empty libraryID to search across all libraries.
func fetchAllSongs(username, libraryID string) ([]subsonicSong, error) {
	const pageSize = 500
	var all []subsonicSong
	offset := 0

	for {
		uri := fmt.Sprintf(
			"search3?query=%%22%%22&songCount=%d&songOffset=%d&albumCount=0&artistCount=0&u=%s",
			pageSize, offset, username)
		if libraryID != "" {
			uri += "&musicFolderId=" + libraryID
		}

		pdk.Log(pdk.LogDebug, fmt.Sprintf(
			"nd-rating-sync: fetching songs – user=%q library=%s offset=%d page_size=%d",
			username, libraryID, offset, pageSize))

		raw, err := host.SubsonicAPICall(uri)
		if err != nil {
			return nil, fmt.Errorf("SubsonicAPICall (offset=%d): %w", offset, err)
		}

		var wrapper subsonicWrapper
		if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
			return nil, fmt.Errorf("unmarshal search3 response: %w", err)
		}
		if wrapper.Response.Status != "ok" {
			if wrapper.Response.Error != nil {
				return nil, fmt.Errorf("Subsonic API error %d: %s",
					wrapper.Response.Error.Code, wrapper.Response.Error.Message)
			}
			return nil, errors.New("Subsonic API returned non-ok status")
		}

		if wrapper.Response.SearchResult3 == nil {
			break
		}
		page := wrapper.Response.SearchResult3.Song
		all = append(all, page...)
		pdk.Log(pdk.LogDebug, fmt.Sprintf(
			"nd-rating-sync: page offset=%d returned %d songs (total so far: %d)",
			offset, len(page), len(all)))

		if len(page) < pageSize {
			break
		}
		offset += pageSize
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf(
		"nd-rating-sync: found %d songs for user=%q library=%s", len(all), username, libraryID))
	return all, nil
}

// setRating calls the Subsonic setRating endpoint.
func setRating(username, songID string, stars int) error {
	uri := fmt.Sprintf("setRating?id=%s&rating=%d&u=%s", songID, stars, username)
	raw, err := host.SubsonicAPICall(uri)
	if err != nil {
		return err
	}

	var wrapper subsonicWrapper
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return fmt.Errorf("unmarshal setRating response: %w", err)
	}
	if wrapper.Response.Status != "ok" {
		if wrapper.Response.Error != nil {
			return fmt.Errorf("API error %d: %s",
				wrapper.Response.Error.Code, wrapper.Response.Error.Message)
		}
		return errors.New("setRating returned non-ok status")
	}
	return nil
}
