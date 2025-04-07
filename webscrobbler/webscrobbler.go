package webscrobbler

import "encoding/json"

type Scrobble struct {
	EventName string `json:"eventName"`
	Time      int64  `json:"time"`
	Data      struct {
		Song struct {
			Connector struct {
				ID      string   `json:"id"`
				JS      string   `json:"js"`
				Label   string   `json:"label"`
				Matches []string `json:"matches"`
			} `json:"connector"`
			ControllerTabID int64 `json:"controllerTabId"`
			Flags           struct {
				FinishedProcessing  bool `json:"finishedProcessing"`
				HasBlockedTag       bool `json:"hasBlockedTag"`
				IsAlbumFetched      bool `json:"isAlbumFetched"`
				IsCorrectedByUser   bool `json:"isCorrectedByUser"`
				IsLovedInService    bool `json:"isLovedInService"`
				IsMarkedAsPlaying   bool `json:"isMarkedAsPlaying"`
				IsRegexEditedByUser struct {
					Album       bool `json:"album"`
					AlbumArtist bool `json:"albumArtist"`
					Artist      bool `json:"artist"`
					Track       bool `json:"track"`
				} `json:"isRegexEditedByUser"`
				IsReplaying bool `json:"isReplaying"`
				IsScrobbled bool `json:"isScrobbled"`
				IsSkipped   bool `json:"isSkipped"`
				IsValid     bool `json:"isValid"`
			} `json:"flags"`
			Metadata struct {
				Label          string `json:"label"`
				StartTimestamp int64  `json:"startTimestamp"`
			} `json:"metadata"`
			NoRegex struct {
				Album       string `json:"album"`
				AlbumArtist string `json:"albumArtist"`
				Artist      string `json:"artist"`
				Duration    string `json:"duration"`
				Track       string `json:"track"`
			} `json:"noRegex"`
			Parsed struct {
				Album                      string `json:"album"`
				AlbumArtist                string `json:"albumArtist"`
				Artist                     string `json:"artist"`
				CurrentTime                int64  `json:"currentTime"`
				Duration                   int64  `json:"duration"`
				IsPlaying                  bool   `json:"isPlaying"`
				IsPodcast                  bool   `json:"isPodcast"`
				OriginURL                  string `json:"originUrl"`
				ScrobblingDisallowedReason string `json:"scrobblingDisallowedReason"`
				Track                      string `json:"track"`
				TrackArt                   string `json:"trackArt"`
				UniqueID                   string `json:"uniqueID"`
			} `json:"parsed"`
			Processed struct {
				Album       string `json:"album"`
				AlbumArtist string `json:"albumArtist"`
				Artist      string `json:"artist"`
				Duration    int64  `json:"duration"`
				Track       string `json:"track"`
			} `json:"processed"`
		} `json:"song"`
	} `json:"data"`
}

func ParseScrobble(data []byte) (Scrobble, error) {
	var scrobble Scrobble
	err := json.Unmarshal(data, &scrobble)
	if err != nil {
		return Scrobble{}, err
	}

	// Ensure isPlaying is set for certain events
	if scrobble.EventName == "nowplaying" ||
		scrobble.EventName == "resumedplaying" ||
		scrobble.EventName == "songchange" {
		// Force these events to be considered "playing"
		scrobble.Data.Song.Parsed.IsPlaying = true
	}

	return scrobble, nil
}
