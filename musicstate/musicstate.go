package musicstate

// NowPlaying represents the current track information from any supported player
type NowPlaying struct {
	Artist   string
	Title    string
	Player   string  // e.g. "TIDAL", "Spotify"
	WindowID uintptr // Platform-specific window identifier
}

// Player defines the interface for music player adapters
type Player interface {
	// Name returns the friendly name of the player (e.g. "TIDAL", "Spotify")
	Name() string

	// ProcessName returns the executable name to search for (e.g. "TIDAL.exe")
	ProcessName() string

	// ParseWindowTitle attempts to extract track info from the window title
	ParseWindowTitle(title string) (*NowPlaying, error)
}

// GetCurrentTrack returns NowPlaying info from the first found supported player
func GetCurrentTrack(players ...Player) (*NowPlaying, error) {
	// Implementation will use winapi package
	return nil, nil
}
