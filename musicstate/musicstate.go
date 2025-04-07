package musicstate

import "errors"

// NowPlaying represents the current track information from any supported player
type NowPlaying struct {
	Artist   string
	Title    string
	Player   string  // e.g. "TIDAL", "Spotify", "Nightbot"
	WindowID uintptr // Platform-specific window identifier (only for window-based players)
}

// Player defines the interface for music player adapters
type Player interface {
	// Name returns the friendly name of the player (e.g. "TIDAL", "Spotify")
	Name() string

	// ProcessName returns the executable name to search for (e.g. "TIDAL.exe")
	// For API-based players, this returns an empty string
	ProcessName() string

	// ParseWindowTitle attempts to extract track info from the window title
	// For API-based players, this returns an error
	ParseWindowTitle(title string) (*NowPlaying, error)
}

// APIPlayer extends the Player interface for players that use external APIs
type APIPlayer interface {
	Player
	// GetCurrentTrack gets the current track directly from the API
	GetCurrentTrack() (*NowPlaying, error)
}

// IsAPIPlayer checks if a player is an API-based player
func IsAPIPlayer(player Player) bool {
	_, ok := player.(APIPlayer)
	return ok
}

// GetCurrentTrack returns NowPlaying info from the first found supported player
func GetCurrentTrack(players ...Player) (*NowPlaying, error) {
	// Implementation will use winapi package
	return nil, errors.New("not implemented")
}
