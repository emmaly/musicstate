package musicstate

import "errors"

var ErrNoCurrentSong = errors.New("no current song playing")

// NowPlaying represents the current track information from any supported player
type NowPlaying struct {
	Artist string
	Title  string
	Player string
}
