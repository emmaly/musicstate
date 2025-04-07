package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
		// Get the origin header
		origin := r.Header.Get("Origin")

		// Allow requests with no origin (like file:// or native applications like OBS)
		if origin == "" {
			return true
		}

		// Parse the origin URL
		originURL, err := url.Parse(origin)
		if err != nil {
			log.Printf("Invalid origin: %s - %v", origin, err)
			return false
		}

		// Get the host from the request
		requestHost := r.Host

		// Allow same-origin requests (same hostname)
		if originURL.Host == requestHost {
			return true
		}

		// Allow localhost for development
		if strings.HasPrefix(originURL.Host, "localhost:") ||
			strings.HasPrefix(originURL.Host, "127.0.0.1:") {
			return true
		}

		// Log rejected origins
		log.Printf("Rejected WebSocket connection from origin: %s", origin)
		return false
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
	CloseOnce sync.Once // Ensures we only close once
}

type Server struct {
	song        *Song
	connections []*ConnectionState
	mu          sync.RWMutex // Use RWMutex for better concurrency
	ctx         context.Context
	cancel      context.CancelFunc
}

// Constants for timeouts and intervals
const (
	DefaultTimeout = 10 * time.Second
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	// Create a root context with cancellation for application-wide shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Clean up WinAPI resources on application exit
	defer winapi.Cleanup()

	// Create a mux for routing
	mux := http.NewServeMux()

	fileServer := http.FileServer(http.FS(staticFiles))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Rewrite the path to include "static"
		r.URL.Path = "/static" + r.URL.Path
		fileServer.ServeHTTP(w, r)
	})

	// Handle WebSocket connections
	mux.HandleFunc("/ws", wsHandler())

	// Try alternative port if default is unavailable
	port := os.Getenv("MUSICSTATE_PORT")
	if port == "" {
		port = "52846"
	}
	hostAddr := "localhost:" + port
	fmt.Printf("SorceressEmmaly's MusicState\nCopyright (C) 2025 emmaly\nSee https://github.com/emmaly/musicstate for documentation, source code, and to file issues.\nThis program is licensed GPLv3; it comes with ABSOLUTELY NO WARRANTY.\nThis is free software, and you are welcome to redistribute it under certain conditions.\nReview license details at https://github.com/emmaly/musicstate/LICENSE\n\n\nUse http://%s as your browser source overlay URL.\n\n\n", hostAddr)

	// Create a server with our mux
	server := &http.Server{
		Addr:    hostAddr,
		Handler: mux,
	}

	// Create a channel for shutdown completion notification
	serverStopCtx, serverStopCtxCancel := context.WithCancel(context.Background())
	defer serverStopCtxCancel() // Ensure cancellation to prevent context leak

	// Handle graceful server shutdown
	go func() {
		<-ctx.Done()
		log.Println("Main context cancelled, shutting down HTTP server...")

		// Create a timeout for server shutdown
		shutdownCtx, shutdownCtxCancel := context.WithTimeout(serverStopCtx, 10*time.Second)
		defer shutdownCtxCancel()

		// Shutdown the server
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}

		serverStopCtxCancel()
	}()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start a goroutine to handle shutdown signals
	go func() {
		sig := <-sigChan
		log.Printf("Received signal: %v", sig)
		cancel() // Cancel the root context
	}()

	// Start the server
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("error starting service: ", err)
	}

	// Wait for server to complete shutdown
	<-serverStopCtx.Done()
	log.Println("Server shutdown complete")
}

func wsHandler() http.HandlerFunc {
	// Create a context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	server := Server{
		ctx:    ctx,
		cancel: cancel,
	}

	// Set up clean shutdown on application exit
	setupCleanShutdown(&server)

	// Start watching for music changes with context
	go server.watchMusic()

	// Start a connection health checker with context
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
		// Create a goroutine to handle context cancellation
		connCtx, connCancel := context.WithCancel(server.ctx)
		defer connCancel()

		// Monitor for server context cancellation
		go func() {
			select {
			case <-server.ctx.Done():
				// Server is shutting down, close this connection
				log.Println("Server shutting down, closing WebSocket connection")
				connState.IsActive = false
				connState.CloseOnce.Do(func() {
					if connState.Conn != nil {
						conn.Close()
					}
				})
				connCancel()
			case <-connCtx.Done():
				// Connection is closing, nothing to do here
				return
			}
		}()

		// Message reading loop
		for {
			select {
			case <-connCtx.Done():
				// Context cancelled, exit the loop
				return
			default:
				// Continue with normal processing
			}

			// Set read deadline
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			// Read message with timeout
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err,
					websocket.CloseGoingAway,
					websocket.CloseNormalClosure) {
					log.Printf("Read error: %v", err)
				}
				connCancel()
				break
			}

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
	// First check if context is already cancelled
	select {
	case <-server.ctx.Done():
		// Context cancelled, don't report music
		return
	default:
		// Context still valid, continue
	}

	// First check if there's any change with a read lock
	server.mu.RLock()
	noChange := (server.song == nil && song == nil) ||
		(server.song != nil &&
			song != nil &&
			server.song.Song == song.Song &&
			server.song.Artist == song.Artist &&
			server.song.AlbumArt == song.AlbumArt)
	hasConnections := server.connections != nil && len(server.connections) > 0
	server.mu.RUnlock()

	// If no changes or no connections, return early without acquiring write lock
	if noChange || !hasConnections {
		return
	}

	// Upgrade to write lock to modify server state
	server.mu.Lock()

	// Double-check state with the write lock (state might have changed)
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

	// Check context again before sending updates
	select {
	case <-server.ctx.Done():
		return
	default:
		// Context still valid, continue
	}

	// Track dead connections to clean up after sending messages
	var deadConnections []*ConnectionState
	var deadConnectionsMu sync.Mutex // Protect the deadConnections slice

	// Use a WaitGroup to track when all sends are done
	var wg sync.WaitGroup

	// Send to all connected clients in parallel
	for _, connState := range connections {
		// Skip nil or already known inactive connections
		if connState == nil || !connState.IsActive || connState.Conn == nil {
			continue
		}

		// Launch goroutine for each send operation
		wg.Add(1)
		go func(connState *ConnectionState) {
			defer wg.Done()

			// Set a write deadline
			connState.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

			// Attempt to send update
			err := connState.Conn.WriteJSON(update)
			if err != nil {
				log.Printf("Write error to client: %v", err)
				connState.IsActive = false

				// Safely add to dead connections
				deadConnectionsMu.Lock()
				deadConnections = append(deadConnections, connState)
				deadConnectionsMu.Unlock()
			}
		}(connState)
	}

	// Wait for all send operations to complete
	wg.Wait()

	// Check context again before cleanup
	select {
	case <-server.ctx.Done():
		return
	default:
		// Context still valid, continue
	}

	// Clean up any connections that failed during this update
	if len(deadConnections) > 0 {
		log.Printf("Cleaning up %d dead connections after update", len(deadConnections))
		for _, connState := range deadConnections {
			if connState != nil {
				server.removeConnection(connState)
				connState.CloseOnce.Do(func() {
					if connState.Conn != nil {
						connState.Conn.Close()
					}
				})
			}
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

	// Create a ticker for regular health checks
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-server.ctx.Done():
			// Context cancelled, exit the goroutine cleanly
			log.Println("Connection health checker shutting down gracefully")
			return

		case <-ticker.C:
			// First check for connections with a read lock
			server.mu.RLock()
			hasConnections := server.connections != nil && len(server.connections) > 0
			server.mu.RUnlock()

			if !hasConnections {
				continue
			}

			// Perform a health check with write lock to get copy of connections
			server.mu.Lock()
			// Double check connections exist with write lock
			if server.connections == nil || len(server.connections) == 0 {
				server.mu.Unlock()
				continue
			}

			// Copy connections to avoid holding the lock during potentially slow operations
			connections := make([]*ConnectionState, len(server.connections))
			copy(connections, server.connections)
			server.mu.Unlock()

			now := time.Now()
			var deadConnections []*ConnectionState
			var deadConnectionsMu sync.Mutex // Protect the deadConnections slice

			// Use a WaitGroup to track ping operations
			var wg sync.WaitGroup

			// Process connections in parallel
			for _, connState := range connections {
				// Skip nil connections
				if connState == nil || connState.Conn == nil {
					continue
				}

				wg.Add(1)
				go func(connState *ConnectionState) {
					defer wg.Done()

					// Check if this connection is already timed out
					if now.Sub(connState.LastPing) > connectionTimeout {
						log.Printf("Connection inactive for %v, marking as dead", now.Sub(connState.LastPing))
						connState.IsActive = false

						deadConnectionsMu.Lock()
						deadConnections = append(deadConnections, connState)
						deadConnectionsMu.Unlock()
						return
					}

					// Check context before sending pings
					select {
					case <-server.ctx.Done():
						return
					default:
						// Context still valid, continue with ping
					}

					// Ping the connection to keep it alive and verify it's still working
					deadline := time.Now().Add(5 * time.Second)
					err := connState.Conn.WriteControl(websocket.PingMessage, []byte{}, deadline)
					if err != nil {
						log.Printf("Failed to ping connection: %v", err)
						connState.IsActive = false

						deadConnectionsMu.Lock()
						deadConnections = append(deadConnections, connState)
						deadConnectionsMu.Unlock()
					}
				}(connState)
			}

			// Wait for all ping operations to complete
			wg.Wait()

			// Check context again before cleanup
			select {
			case <-server.ctx.Done():
				return
			default:
				// Context still valid, continue with cleanup
			}

			// Clean up dead connections
			if len(deadConnections) > 0 {
				log.Printf("Found %d dead connections to clean up", len(deadConnections))
				for _, connState := range deadConnections {
					if connState != nil {
						log.Println("Cleaning up dead connection")
						server.removeConnection(connState)

						// Safe close with nil check
						connState.CloseOnce.Do(func() {
							if connState.Conn != nil {
								connState.Conn.Close()
							}
						})
					}
				}
			}
		}
	}
}

// findConnection looks up a connection by its conn pointer
func (server *Server) findConnection(conn *websocket.Conn) *ConnectionState {
	server.mu.RLock() // Use Read Lock for better concurrency
	defer server.mu.RUnlock()

	for _, connState := range server.connections {
		if connState != nil && connState.Conn == conn {
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

	// Safety check for nil or empty connections slice
	if server.connections == nil || len(server.connections) == 0 {
		return
	}

	// If we're removing the only connection, just set to empty slice
	if len(server.connections) == 1 && server.connections[0] == connState {
		log.Println("Removed last connection, connections now empty")
		server.connections = make([]*ConnectionState, 0)
		return
	}

	// Make a copy of the slice to avoid memory leaks
	// Use max(0, len-1) to ensure capacity is never negative
	capacity := len(server.connections)
	if capacity > 0 {
		capacity--
	}

	newConnections := make([]*ConnectionState, 0, capacity)

	// Only append connections that don't match the one we're removing
	var removed bool
	for _, cs := range server.connections {
		if cs != connState {
			newConnections = append(newConnections, cs)
		} else {
			removed = true
		}
	}

	// If we actually removed a connection, log it
	if removed {
		log.Printf("Removed connection, remaining connections: %d", len(newConnections))
	} else {
		log.Println("Connection not found in list, no connection removed")
	}

	server.connections = newConnections
}

// setupCleanShutdown sets up signal handling for graceful shutdown
func setupCleanShutdown(server *Server) {
	// Create a channel to listen for OS signals
	sigChan := make(chan os.Signal, 1)

	// Register for signal notifications
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start a goroutine to handle shutdown signals
	go func() {
		// Wait for termination signal
		sig := <-sigChan
		log.Printf("Received shutdown signal: %v", sig)

		// Initiate graceful shutdown
		log.Println("Starting graceful shutdown...")

		// Cancel the context to notify all goroutines
		if server.cancel != nil {
			server.cancel()
		}

		// Close all WebSocket connections
		server.mu.Lock()
		connections := server.connections
		server.connections = nil
		server.mu.Unlock()

		// Close all active connections
		for _, connState := range connections {
			if connState != nil {
				connState.IsActive = false
				connState.CloseOnce.Do(func() {
					if connState.Conn != nil {
						log.Println("Closing WebSocket connection during shutdown")
						connState.Conn.Close()
					}
				})
			}
		}

		log.Println("Graceful shutdown completed")
	}()
}

// FindSpotifyTitle finds Spotify window title
func FindSpotifyTitle() (string, error) {
	// Find Spotify windows directly
	spotifyWindows, err := winapi.FindWindowsByProcess(
		[]string{"Spotify.exe"},
		winapi.WinVisible(true),
	)

	if err != nil {
		return "", err
	}

	// Get the window title
	if len(spotifyWindows) > 0 {
		for _, w := range spotifyWindows {
			if strings.TrimSpace(w.Title) != "" {
				return w.Title, nil
			}
		}
	}

	return "", fmt.Errorf("no Spotify window with title found")
}

// FindTidalTitle finds TIDAL window title
func FindTidalTitle() (string, error) {
	// Find TIDAL windows directly
	tidalWindows, err := winapi.FindWindowsByProcess(
		[]string{"TIDAL.exe"},
		winapi.WinVisible(true),
	)

	if err != nil {
		return "", err
	}

	// Get the window title
	if len(tidalWindows) > 0 {
		for _, w := range tidalWindows {
			if strings.TrimSpace(w.Title) != "" {
				return w.Title, nil
			}
		}
	}

	return "", fmt.Errorf("no TIDAL window with title found")
}

// FindAppleMusicTitle finds Apple Music window title
func FindAppleMusicTitle() (string, error) {
	// Known browser processes
	browserProcesses := []string{
		"chrome.exe",
		"msedge.exe",
		"firefox.exe",
		"opera.exe",
		"brave.exe",
	}

	// Find browser windows with Apple Music
	for _, browser := range browserProcesses {
		windows, err := winapi.FindWindowsByProcess(
			[]string{browser},
			winapi.WinVisible(true),
		)

		if err == nil && len(windows) > 0 {
			// Check for Apple Music in title
			for _, w := range windows {
				title := w.Title

				// Only consider titles that have "Apple Music"
				if strings.Contains(title, "Apple Music") {
					// Music pages typically have both " by " and at least 2 hyphens
					// For example: "Song - Album - Apple Music" or "Song - Single by Artist - Apple Music"
					if strings.Contains(title, " by ") && strings.Count(title, "-") >= 2 {
						return title, nil
					}
				}
			}
		}
	}

	return "", fmt.Errorf("no browser window with Apple Music found")
}

// ParseSpotifyTitle parses a Spotify window title
func ParseSpotifyTitle(title string) *Song {
	if title == "" {
		return nil
	}

	// Check if it's just the application name
	if title == "Spotify" || title == "Spotify Premium" {
		return nil
	}

	// Split by " - " if possible
	parts := strings.Split(title, " - ")

	if len(parts) >= 2 {
		return &Song{
			Artist: parts[0],
			Song:   strings.Join(parts[1:], " - "),
		}
	}

	// If no delimiter, just use the title as song name
	return &Song{
		Artist: "Unknown Artist",
		Song:   title,
	}
}

// ParseTidalTitle parses a TIDAL window title
func ParseTidalTitle(title string) *Song {
	if title == "" {
		return nil
	}

	// Check if it's just the application name
	if title == "TIDAL" {
		return nil
	}

	// Split by " - " if possible
	parts := strings.Split(title, " - ")

	if len(parts) >= 2 {
		return &Song{
			Artist: parts[len(parts)-1],
			Song:   strings.Join(parts[:len(parts)-1], " - "),
		}
	}

	// If no delimiter, just use the title as song name
	return &Song{
		Artist: "Unknown Artist",
		Song:   title,
	}
}

// ParseAppleMusicTitle parses an Apple Music window title
func ParseAppleMusicTitle(title string) *Song {
	if title == "" || !strings.Contains(title, "Apple Music") {
		return nil
	}

	// Remove browser suffix
	appleIndex := strings.Index(title, "Apple Music")
	if appleIndex > 0 {
		title = title[:appleIndex+11]
	}

	// Remove " - Apple Music" suffix
	title = strings.TrimSuffix(title, " - Apple Music")
	title = strings.TrimSpace(title)

	// Try to find artist with " by " separator
	byParts := strings.Split(title, " by ")
	if len(byParts) >= 2 {
		artist := strings.TrimSpace(byParts[len(byParts)-1])

		// Handle the song title part
		songTitle := strings.Join(byParts[:len(byParts)-1], " by ")

		// Clean up song title
		// Remove " - Album" or " - Single" if present
		for _, suffix := range []string{" - Album", " - Single", " - EP"} {
			if idx := strings.LastIndex(songTitle, suffix); idx >= 0 {
				songTitle = songTitle[:idx]
				break
			}
		}

		// Clean up any invisible characters
		if len(songTitle) > 0 && songTitle[0] < 32 {
			songTitle = songTitle[1:]
		}

		return &Song{
			Artist: artist,
			Song:   strings.TrimSpace(songTitle),
		}
	}

	// Fallback if no " by " found
	return &Song{
		Artist: "Unknown Artist",
		Song:   title,
	}
}

func (server *Server) watchMusic() {
	log.Println("Starting watchMusic function...")

	// Add panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Printf("RECOVERED from panic in watchMusic: %v", r)
			// Wait a bit and restart
			time.Sleep(2 * time.Second)
			go server.watchMusic()
		}
	}()

	const (
		defaultNightbotPoll = 5               // seconds
		windowPollInterval  = 2 * time.Second // Increased to reduce CPU usage
	)

	// Allow configuring Nightbot poll interval with environment variable
	nightbotPollSeconds, err := strconv.Atoi(os.Getenv("NIGHTBOT_POLL_INTERVAL"))
	if err != nil || nightbotPollSeconds < 1 {
		nightbotPollSeconds = defaultNightbotPoll
	}
	nightbotPollInterval := time.Duration(nightbotPollSeconds) * time.Second
	debugLog("Nightbot poll interval set to %v", nightbotPollInterval)

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
			log.Println("Nightbot disabled: missing NIGHTBOT_CLIENT_ID")
		}
		if clientSecret == "" {
			log.Println("Nightbot disabled: missing NIGHTBOT_CLIENT_SECRET")
		}
		if redirectURL == "" {
			log.Println("Nightbot disabled: missing NIGHTBOT_REDIRECT_URL")
		}
		if tokenJSON == "" {
			log.Println("Nightbot disabled: missing NIGHTBOT_TOKEN")
		}
	}

	// Create a ticker for regular polling
	windowTicker := time.NewTicker(windowPollInterval)
	defer windowTicker.Stop()

	log.Println("Starting music watch loop...")
	tickCount := 0

	// Use the server's context for cancellation
	for {
		tickCount++

		select {
		case <-server.ctx.Done():
			// Context cancelled, exit the goroutine cleanly
			log.Println("Watch music goroutine shutting down gracefully")
			return

		case <-windowTicker.C:
			debugLog("Processing tick #%d", tickCount)

			// Regular polling tick
			var song *Song
			now := time.Now()

			// First try API-based players (like Nightbot) but only every 5 seconds
			if nightbotPlayer != nil && now.Sub(lastNightbotPoll) >= nightbotPollInterval {
				debugLog("Polling Nightbot (last poll was %v ago)", now.Sub(lastNightbotPoll))
				lastNightbotPoll = now

				// Make the API call with appropriate timeout
				_, apiCancel := context.WithTimeout(server.ctx, DefaultTimeout)
				nowPlaying, err := nightbotPlayer.GetCurrentTrack()
				apiCancel()

				if err == nil && nowPlaying != nil {
					lastNightbotSong = &Song{
						Artist: nowPlaying.Artist,
						Song:   nowPlaying.Title,
					}
					debugLog("Updated Nightbot song from API")
				} else {
					// Clear the cached song when there's an error or no song playing
					if lastNightbotSong != nil {
						debugLog("Clearing previously cached Nightbot song")
					}
					lastNightbotSong = nil

					if err != nil {
						if errors.Is(err, nightbot.ErrNoCurrentSong) {
							debugLog("No current song playing in Nightbot")
						} else {
							debugLog("Nightbot error: %v", err)
						}
					} else {
						debugLog("No current song data from Nightbot")
					}
				}
			}

			// Use the cached Nightbot song if available
			if lastNightbotSong != nil {
				song = lastNightbotSong
			}

			// Always check for window-based players to detect when songs stop
			var foundWindowSong bool

			// Try to find Spotify
			debugLog("Checking Spotify...")
			spotifyTitle, spotifyErr := FindSpotifyTitle()
			if spotifyErr == nil && spotifyTitle != "" {
				debugLog("Found Spotify title: %s", spotifyTitle)
				if spotifySong := ParseSpotifyTitle(spotifyTitle); spotifySong != nil {
					foundWindowSong = true
					if song == nil {
						song = spotifySong
						debugLog("Using Spotify song: %s - %s", song.Artist, song.Song)
					}
				}
			}

			// Try to find TIDAL if no song yet
			if !foundWindowSong {
				debugLog("Checking TIDAL...")
				tidalTitle, tidalErr := FindTidalTitle()
				if tidalErr == nil && tidalTitle != "" {
					debugLog("Found TIDAL title: %s", tidalTitle)
					if tidalSong := ParseTidalTitle(tidalTitle); tidalSong != nil {
						foundWindowSong = true
						if song == nil {
							song = tidalSong
							debugLog("Using TIDAL song: %s - %s", song.Artist, song.Song)
						}
					}
				}
			}

			// Try to find Apple Music if no song yet
			if !foundWindowSong {
				debugLog("Checking Apple Music...")
				appleMusicTitle, appleErr := FindAppleMusicTitle()
				if appleErr == nil && appleMusicTitle != "" {
					debugLog("Found Apple Music title: %s", appleMusicTitle)
					if appleSong := ParseAppleMusicTitle(appleMusicTitle); appleSong != nil {
						foundWindowSong = true
						if song == nil {
							song = appleSong
							debugLog("Using Apple Music song: %s - %s", song.Artist, song.Song)
						}
					}
				}
			}

			// If no window song found and last Nightbot poll was a while ago,
			// clear the lastNightbotSong to ensure we don't keep stale state
			if !foundWindowSong && lastNightbotSong != nil && now.Sub(lastNightbotPoll) >= (nightbotPollInterval*2) {
				debugLog("No window song found and Nightbot poll is old - clearing song cache")
				lastNightbotSong = nil
				song = nil
			}

			if song != nil && song.Song != "" && song.AlbumArt == "" {
				// set the placeholder album art
				// Use a relative URL for the album art so it works with any host/port
				song.AlbumArt = "/images/album.jpg"
			}

			// Log the final decision
			if song != nil {
				debugLog("Reporting song: %s - %s", song.Artist, song.Song)
			} else {
				debugLog("No song found to report")
			}

			server.reportMusic(song)
		}
	}
}

func debugLog(msg string, args ...interface{}) {
	if os.Getenv("DEBUG") == "" {
		return
	}

	if len(args) > 0 {
		log.Printf(msg, args...)
	} else {
		log.Println(msg)
	}
}
