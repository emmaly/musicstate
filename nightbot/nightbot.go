package nightbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/emmaly/musicstate/musicstate"
	"golang.org/x/oauth2"
)

const (
	// NightbotAPIBase is the base URL for Nightbot API
	NightbotAPIBase = "https://api.nightbot.tv/1"
	// DefaultTimeout for API requests
	DefaultTimeout = 10 * time.Second
)

// Client represents a Nightbot API client
type Client struct {
	httpClient *http.Client
	config     *oauth2.Config
	token      *oauth2.Token
}

// SongRequestQueue represents the Nightbot song request queue
type SongRequestQueue struct {
	CurrentSong *SongRequest `json:"_currentSong"`
	Queue       []SongRequest `json:"_queue"`
}

// SongRequest represents a song in the queue
type SongRequest struct {
	Track Track `json:"track"`
	User  User  `json:"user"`
}

// Track represents a song track
type Track struct {
	Provider   string `json:"provider"`
	ProviderID string `json:"providerId"`
	Duration   int    `json:"duration"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	URL        string `json:"url"`
}

// User represents the user who requested the song
type User struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	UserID   string `json:"_id"`
}

// NewClient creates a new Nightbot API client
func NewClient(clientID, clientSecret, redirectURL string, token *oauth2.Token) *Client {
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://api.nightbot.tv/oauth2/authorize",
			TokenURL: "https://api.nightbot.tv/oauth2/token",
		},
		Scopes: []string{"song_requests_queue"},
	}

	// Create HTTP client with the token
	httpClient := config.Client(context.Background(), token)

	return &Client{
		httpClient: httpClient,
		config:     config,
		token:      token,
	}
}

// ErrNoCurrentSong is returned when there's no active song playing
var ErrNoCurrentSong = errors.New("no current song playing")

// GetCurrentSong returns the current playing song
func (c *Client) GetCurrentSong() (*musicstate.NowPlaying, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", NightbotAPIBase+"/song_requests/queue", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	log.Println("Fetching Nightbot song queue...")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("Nightbot API request error: %v", err)
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Nightbot API error (status %d): %s", resp.StatusCode, body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, body)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	
	// Log the full API response
	log.Printf("Nightbot API response: %s", string(bodyBytes))

	// Parse the response into our struct
	var queue SongRequestQueue
	if err := json.Unmarshal(bodyBytes, &queue); err != nil {
		log.Printf("Error decoding Nightbot response: %v", err)
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	if queue.CurrentSong == nil {
		log.Println("No current song playing in Nightbot")
		return nil, ErrNoCurrentSong
	}

	// Include requester name in the log
	log.Printf("Current Nightbot song: %s - %s (requested by %s)", 
		queue.CurrentSong.Track.Artist, 
		queue.CurrentSong.Track.Title,
		queue.CurrentSong.User.Name)

	// Format artist with requester information
	artistWithRequester := fmt.Sprintf("%s (requested by %s)", 
		queue.CurrentSong.Track.Artist, 
		queue.CurrentSong.User.Name)

	return &musicstate.NowPlaying{
		Artist: artistWithRequester,
		Title:  queue.CurrentSong.Track.Title,
		Player: "Nightbot",
	}, nil
}

// RefreshToken refreshes the OAuth token if needed
func (c *Client) RefreshToken() error {
	if c.token == nil {
		log.Println("No token available for refresh")
		return errors.New("no token available")
	}
	
	if !c.token.Valid() {
		log.Println("Token expired, attempting to refresh...")
		newToken, err := c.config.TokenSource(context.Background(), c.token).Token()
		if err != nil {
			log.Printf("Error refreshing token: %v", err)
			return fmt.Errorf("refreshing token: %w", err)
		}

		log.Println("Token successfully refreshed")
		c.token = newToken
		c.httpClient = c.config.Client(context.Background(), newToken)
		return nil
	}
	
	// Token is still valid
	log.Printf("Token valid until: %v", c.token.Expiry)
	return nil
}

// NightbotPlayer implements the musicstate.Player interface for Nightbot
type NightbotPlayer struct {
	client *Client
}

// NewNightbotPlayer creates a new Nightbot player
func NewNightbotPlayer(clientID, clientSecret, redirectURL string, token *oauth2.Token) *NightbotPlayer {
	client := NewClient(clientID, clientSecret, redirectURL, token)
	return &NightbotPlayer{client: client}
}

// Name returns the name of the player
func (p *NightbotPlayer) Name() string {
	return "Nightbot"
}

// ProcessName returns the process name (not used for API-based players)
func (p *NightbotPlayer) ProcessName() string {
	return "" // Not applicable for API-based players
}

// ParseWindowTitle is not used for API-based players
func (p *NightbotPlayer) ParseWindowTitle(title string) (*musicstate.NowPlaying, error) {
	return nil, errors.New("window title parsing not supported for Nightbot")
}

// GetCurrentTrack gets the current track from Nightbot API
func (p *NightbotPlayer) GetCurrentTrack() (*musicstate.NowPlaying, error) {
	log.Println("Nightbot player: checking for current track")
	
	// Check if token needs refresh
	if p.client.token != nil {
		now := time.Now()
		log.Printf("Token expiry: %v (in %v)", p.client.token.Expiry, p.client.token.Expiry.Sub(now))
		log.Printf("Token: access=%s... refresh=%s...", 
			p.client.token.AccessToken[:10], 
			p.client.token.RefreshToken[:10])
		
		if !p.client.token.Valid() {
			log.Println("Token needs refresh")
			if err := p.client.RefreshToken(); err != nil {
				log.Printf("Failed to refresh token: %v", err)
				return nil, fmt.Errorf("refreshing token: %w", err)
			}
		} else {
			log.Println("Token is still valid")
		}
	} else {
		log.Println("No token available")
		return nil, errors.New("no token available")
	}

	// Get current song from Nightbot API
	track, err := p.client.GetCurrentSong()
	if err != nil {
		if errors.Is(err, ErrNoCurrentSong) {
			log.Println("No active songs in Nightbot queue")
		} else {
			log.Printf("Error fetching Nightbot song: %v", err)
		}
		return nil, err
	}
	
	log.Printf("Successfully retrieved Nightbot track: %s - %s", track.Artist, track.Title)
	return track, nil
}