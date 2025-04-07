package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emmaly/musicstate/nightbot"
	"github.com/emmaly/musicstate/winapi"
	"github.com/gorilla/websocket"
	"golang.org/x/oauth2"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins
	},
}

type Song struct {
	Artist   string `json:"artist"`
	Song     string `json:"song"`
	AlbumArt string `json:"albumArt"`
}

type SongUpdate struct {
	Type     string `json:"type"`     // "change" or "stop"
	Artist   string `json:"artist"`   // Only used for "change"
	Song     string `json:"song"`     // Only used for "change"
	AlbumArt string `json:"albumArt"` // Only used for "change"
}

// ConnectionState represents the state of a WebSocket connection
type ConnectionState struct {
	Conn      *websocket.Conn
	IsActive  bool
	LastPing  time.Time
	CloseOnce sync.Once  // Ensures we only close once
}

type Server struct {
	song        *Song
	connections []*ConnectionState
	mu          sync.Mutex
}

//go:embed static/*
var staticFiles embed.FS

func main() {
	fileServer := http.FileServer(http.FS(staticFiles))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Rewrite the path to include "static"
		r.URL.Path = "/static" + r.URL.Path
		fileServer.ServeHTTP(w, r)
	})

	// Handle WebSocket connections
	http.HandleFunc("/ws", wsHandler())

	// Try alternative port if default is unavailable
	port := os.Getenv("MUSICSTATE_PORT")
	if port == "" {
		port = "52846"
	}
	hostAddr := "localhost:" + port
	fmt.Printf("SorceressEmmaly's MusicState\nCopyright (C) 2025 emmaly\nSee https://github.com/emmaly/musicstate for documentation, source code, and to file issues.\nThis program is licensed GPLv3; it comes with ABSOLUTELY NO WARRANTY.\nThis is free software, and you are welcome to redistribute it under certain conditions.\nReview license details at https://github.com/emmaly/musicstate/LICENSE\n\n\nUse http://%s as your browser source overlay URL.\n\n\n", hostAddr)
	err := http.ListenAndServe(hostAddr, nil)
	if err != nil {
		log.Fatal("error starting service: ", err)
	}
}

func wsHandler() http.HandlerFunc {
	server := Server{}

	// Start watching for music changes
	go server.watchMusic()
	
	// Start a connection health checker
	go server.connectionHealthCheck()

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Upgrade error:", err)
			return
		}
		
		// Set up ping handler to track connection health
		conn.SetPingHandler(func(appData string) error {
			// Update the connection's last ping time
			connState := server.findConnection(conn)
			if connState != nil {
				server.mu.Lock()
				connState.LastPing = time.Now()
				connState.IsActive = true
				server.mu.Unlock()
			}
			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})
		
		// Set reasonable timeouts
		conn.SetReadLimit(1024) // Limit incoming message sizes
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error {
			// Reset the read deadline when we get a pong
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			return nil
		})

		// Create connection state and add to server
		connState := &ConnectionState{
			Conn:     conn,
			IsActive: true,
			LastPing: time.Now(),
		}
		server.addConnection(connState)
		
		// Ensure connection is properly cleaned up when this handler exits
		defer func() {
			server.removeConnection(connState) 
			connState.CloseOnce.Do(func() {
				conn.Close()
				log.Println("WebSocket connection closed and cleaned up")
			})
		}()

		// Send the current state to the new connection
		var update SongUpdate
		server.mu.Lock()
		if server.song != nil {
			update = server.song.AsSongUpdate()
			log.Printf("Sending current song to new connection: %s - %s", server.song.Artist, server.song.Song)
		} else {
			update = SongUpdate{Type: "stop"}
			log.Println("Sending stop state to new connection (no song playing)")
		}
		server.mu.Unlock()
		
		err = conn.WriteJSON(update)
		if err != nil {
			log.Println("Initial write error:", err)
			return
		}

		// Keep connection alive and handle any incoming messages
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, 
					websocket.CloseGoingAway, 
					websocket.CloseNormalClosure) {
					log.Printf("Read error: %v", err)
				}
				break
			}
			
			// Reset deadlines on successful read
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			
			// Update last activity timestamp
			server.mu.Lock()
			connState.LastPing = time.Now()
			connState.IsActive = true
			server.mu.Unlock()
		}
	}
}

func (s *Song) AsSongUpdate() SongUpdate {
	if s != nil {
		return SongUpdate{
			Type:     "change",
			Artist:   s.Artist,
			Song:     s.Song,
			AlbumArt: s.AlbumArt,
		}
	}

	return SongUpdate{Type: "stop"}
}

func (server *Server) reportMusic(song *Song) {
	server.mu.Lock()

	// Check if there's any change that requires updating clients
	if (server.song == nil && song == nil) ||
		(server.song != nil &&
			song != nil &&
			server.song.Song == song.Song &&
			server.song.Artist == song.Artist &&
			server.song.AlbumArt == song.AlbumArt) {
		server.mu.Unlock()
		return // nothing to do
	}

	// Update the stored song
	server.song = song

	// Check if we have any connections
	if server.connections == nil || len(server.connections) == 0 {
		server.mu.Unlock()
		return
	}

	// Create the appropriate update based on whether there's a song or not
	var update SongUpdate
	if song == nil {
		update = SongUpdate{Type: "stop"}
		log.Println("Sending stop update to clients (no song playing)")
	} else {
		update = song.AsSongUpdate()
		log.Printf("Sending song update: %s - %s", song.Artist, song.Song)
	}

	// Copy connection slice to avoid holding the lock during writes
	connections := make([]*ConnectionState, len(server.connections))
	copy(connections, server.connections)
	server.mu.Unlock()

	// Track dead connections to clean up after sending messages
	var deadConnections []*ConnectionState
	
	// Send to all connected clients
	for _, connState := range connections {
		// Skip already known inactive connections
		if !connState.IsActive {
			continue
		}
		
		// Set a write deadline
		connState.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		
		err := connState.Conn.WriteJSON(update)
		if err != nil {
			log.Printf("Write error to client: %v", err)
			connState.IsActive = false
			deadConnections = append(deadConnections, connState)
		}
	}
	
	// Clean up any connections that failed during this update
	if len(deadConnections) > 0 {
		log.Printf("Cleaning up %d dead connections after update", len(deadConnections))
		for _, connState := range deadConnections {
			server.removeConnection(connState)
			connState.CloseOnce.Do(func() {
				connState.Conn.Close()
			})
		}
	}
}

// connectionHealthCheck periodically checks connection health and cleans up dead connections
func (server *Server) connectionHealthCheck() {
	const (
		healthCheckInterval = 30 * time.Second
		connectionTimeout   = 120 * time.Second
	)
	
	log.Println("Starting WebSocket connection health checker")
	
	for {
		time.Sleep(healthCheckInterval)
		
		server.mu.Lock()
		if server.connections == nil || len(server.connections) == 0 {
			server.mu.Unlock()
			continue
		}
		
		now := time.Now()
		deadConnections := []*ConnectionState{}
		
		// Identify dead connections
		for _, connState := range server.connections {
			// If it's been too long since the last ping/activity
			if now.Sub(connState.LastPing) > connectionTimeout {
				log.Printf("Connection inactive for %v, marking as dead", now.Sub(connState.LastPing))
				connState.IsActive = false
				deadConnections = append(deadConnections, connState)
			} else {
				// Ping the connection to keep it alive and verify it's still working
				deadline := time.Now().Add(5 * time.Second)
				err := connState.Conn.WriteControl(websocket.PingMessage, []byte{}, deadline)
				if err != nil {
					log.Printf("Failed to ping connection: %v", err)
					connState.IsActive = false
					deadConnections = append(deadConnections, connState)
				}
			}
		}
		
		server.mu.Unlock()
		
		// Clean up dead connections
		for _, connState := range deadConnections {
			log.Println("Cleaning up dead connection")
			server.removeConnection(connState)
			
			// Safe close
			connState.CloseOnce.Do(func() {
				connState.Conn.Close()
			})
		}
	}
}

// findConnection looks up a connection by its conn pointer
func (server *Server) findConnection(conn *websocket.Conn) *ConnectionState {
	server.mu.Lock()
	defer server.mu.Unlock()
	
	for _, connState := range server.connections {
		if connState.Conn == conn {
			return connState
		}
	}
	return nil
}

func (server *Server) addConnection(connState *ConnectionState) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.connections == nil {
		server.connections = make([]*ConnectionState, 0, 10) // Pre-allocate some capacity
	}

	server.connections = append(server.connections, connState)
	log.Printf("Added new connection, total connections: %d", len(server.connections))
}

func (server *Server) removeConnection(connState *ConnectionState) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.connections == nil {
		return
	}

	// Make a copy of the slice to avoid memory leaks
	newConnections := make([]*ConnectionState, 0, len(server.connections)-1)
	
	for _, cs := range server.connections {
		if cs != connState {
			newConnections = append(newConnections, cs)
		}
	}
	
	// If we removed a connection, log it
	if len(newConnections) < len(server.connections) {
		log.Printf("Removed connection, remaining connections: %d", len(newConnections))
	}
	
	server.connections = newConnections
}

func (server *Server) watchMusic() {
	const (
		tidal              = "TIDAL.exe"
		spotify            = "Spotify.exe"
		defaultNightbotPoll = 5 // seconds
		windowPollInterval = 1 * time.Second
	)
	
	// Allow configuring Nightbot poll interval with environment variable
	nightbotPollSeconds, err := strconv.Atoi(os.Getenv("NIGHTBOT_POLL_INTERVAL"))
	if err != nil || nightbotPollSeconds < 1 {
		nightbotPollSeconds = defaultNightbotPoll
	}
	nightbotPollInterval := time.Duration(nightbotPollSeconds) * time.Second
	log.Printf("Nightbot poll interval set to %v", nightbotPollInterval)

	// Create a Nightbot player if environment variables are set
	var nightbotPlayer *nightbot.NightbotPlayer
	var lastNightbotPoll time.Time
	var lastNightbotSong *Song
	
	clientID := os.Getenv("NIGHTBOT_CLIENT_ID")
	clientSecret := os.Getenv("NIGHTBOT_CLIENT_SECRET")
	redirectURL := os.Getenv("NIGHTBOT_REDIRECT_URL")
	tokenJSON := os.Getenv("NIGHTBOT_TOKEN")

	// If all Nightbot environment variables are set, initialize the player
	if clientID != "" && clientSecret != "" && redirectURL != "" && tokenJSON != "" {
		log.Println("Nightbot configuration found")
		// Parse the token from JSON
		var token oauth2.Token
		if err := json.Unmarshal([]byte(tokenJSON), &token); err != nil {
			log.Printf("Error parsing Nightbot token: %v", err)
			log.Printf("Token JSON: %s", tokenJSON)
		} else {
			log.Printf("Initializing Nightbot player with token (expires: %v)", token.Expiry)
			nightbotPlayer = nightbot.NewNightbotPlayer(clientID, clientSecret, redirectURL, &token)
			log.Println("Nightbot player initialized successfully")
		}
	} else {
		if clientID == "" {
			log.Println("Missing NIGHTBOT_CLIENT_ID")
		}
		if clientSecret == "" {
			log.Println("Missing NIGHTBOT_CLIENT_SECRET")
		}
		if redirectURL == "" {
			log.Println("Missing NIGHTBOT_REDIRECT_URL")
		}
		if tokenJSON == "" {
			log.Println("Missing NIGHTBOT_TOKEN")
		}
	}

	for {
		var song *Song
		now := time.Now()

		// First try API-based players (like Nightbot) but only every 5 seconds
		if nightbotPlayer != nil && now.Sub(lastNightbotPoll) >= nightbotPollInterval {
			log.Printf("Polling Nightbot (last poll was %v ago)", now.Sub(lastNightbotPoll))
			lastNightbotPoll = now
			
			nowPlaying, err := nightbotPlayer.GetCurrentTrack()
			if err == nil && nowPlaying != nil {
				lastNightbotSong = &Song{
					Artist: nowPlaying.Artist,
					Song:   nowPlaying.Title,
				}
				log.Println("Updated Nightbot song from API")
			} else {
				// Clear the cached song when there's an error or no song playing
				if lastNightbotSong != nil {
					log.Println("Clearing previously cached Nightbot song")
				}
				lastNightbotSong = nil
				
				if err != nil {
					if errors.Is(err, nightbot.ErrNoCurrentSong) {
						log.Println("No current song playing in Nightbot")
					} else {
						log.Printf("Nightbot error: %v", err)
					}
				} else {
					log.Println("No current song data from Nightbot")
				}
			}
		}
		
		// Use the cached Nightbot song if available
		if lastNightbotSong != nil {
			song = lastNightbotSong
		}
		
		// Always check for window-based players to detect when songs stop
		windows, err := winapi.FindWindowsByProcess(
			[]string{tidal, spotify},
			winapi.WinVisible(true),
			winapi.WinClass("Chrome_WidgetWin_1"),
			winapi.WinTitlePattern(*regexp.MustCompile("-")),
		)
		
		var foundWindowSong bool
		if err != nil {
			log.Printf("Error finding windows: %v", err)
		} else {
			for _, w := range windows {
				delimiter := " - "
				songParts := strings.Split(w.Title, delimiter)
				if len(songParts) >= 2 {
					foundWindowSong = true
					if song == nil { // Only use window song if we don't have a Nightbot song
						if w.ProcessName == tidal {
							song = &Song{
								Artist: songParts[len(songParts)-1],
								Song:   strings.Join(songParts[:len(songParts)-1], delimiter),
							}
						} else if w.ProcessName == spotify {
							song = &Song{
								Artist: songParts[0],
								Song:   strings.Join(songParts[1:], delimiter),
							}
						}
					}
				}
				if song != nil && foundWindowSong {
					break
				}
			}
			
			// If no window song found and last Nightbot poll was a while ago,
			// clear the lastNightbotSong to ensure we don't keep stale state
			if !foundWindowSong && lastNightbotSong != nil && now.Sub(lastNightbotPoll) >= (nightbotPollInterval*2) {
				log.Println("No window song found and Nightbot poll is old - clearing song cache")
				lastNightbotSong = nil
				song = nil
			}
		}

		if song != nil && song.Song != "" && song.AlbumArt == "" {
			// set the placeholder album art
			// Use a relative URL for the album art so it works with any host/port
			song.AlbumArt = "/images/album.jpg"
		}

		server.reportMusic(song)

		// Always sleep for the window poll interval (1 second)
		// Nightbot polling is controlled by the timestamp check above
		time.Sleep(windowPollInterval)
	}
}
