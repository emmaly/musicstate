package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emmaly/musicstate/musicstate"
	"github.com/emmaly/musicstate/nightbot"
	"github.com/emmaly/musicstate/webscrobbler"
	"github.com/gorilla/websocket"
	"golang.org/x/oauth2"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins
		origin := r.Header.Get("Origin")

		// Allow requests with no origin (like mobile apps or curl)
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
	song             *Song
	connections      []*ConnectionState
	webScrobbler     *webscrobbler.Scrobble
	nightbotSong     *Song
	lastNightbotPoll time.Time
	mu               sync.RWMutex // Use RWMutex for better concurrency
	ctx              context.Context
	cancel           context.CancelFunc
}

// Constants for timeouts and intervals
const (
	DefaultTimeout      = 10 * time.Second
	WebScrobblerTimeout = 3 * time.Minute // Timeout for Web Scrobbler data
	DefaultNightbotPoll = 5 * time.Second // Default poll interval for Nightbot
)

//go:embed static/*
var staticFiles embed.FS

// Global server instance
var globalServer *Server

// Global Nightbot player
var nightbotPlayer *nightbot.NightbotPlayer

// handleHTTPPost processes POST requests from Web Scrobbler
func handleHTTPPost(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received POST request from %s", r.RemoteAddr)

	// Only handle POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read the request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	// Check if it's valid JSON
	if !json.Valid(bodyBytes) {
		log.Printf("Invalid JSON in POST request")
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Parse the JSON
	scrobble, err := webscrobbler.ParseScrobble(bodyBytes)
	if err != nil {
		log.Printf("Error parsing JSON: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	log.Printf("Processing Web Scrobbler event: %s", scrobble.EventName)
	log.Printf("Song details: Artist=%s, Track=%s, IsPlaying=%v",
		scrobble.Data.Song.Parsed.Artist,
		scrobble.Data.Song.Parsed.Track,
		scrobble.Data.Song.Parsed.IsPlaying)

	if globalServer != nil {
		// Process the Web Scrobbler data
		processWebScrobblerData(globalServer, &scrobble)
	}

	// Return success
	w.WriteHeader(http.StatusOK)
}

func main() {
	// Create a root context with cancellation for application-wide shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the global server
	globalServer = &Server{
		ctx:    ctx,
		cancel: cancel,
	}

	// Initialize Nightbot if environment variables are set
	initNightbot()

	// Start Web Scrobbler timeout checker
	go globalServer.checkWebScrobblerTimeout()

	// Start Nightbot music watcher if configured
	if nightbotPlayer != nil {
		go globalServer.watchNightbotMusic()
	}

	// Create a mux for routing
	mux := http.NewServeMux()

	// Initialize a file logger for POST requests
	logDir := os.Getenv("MUSICSTATE_LOG_DIR")
	if logDir == "" {
		// Use the executable directory by default
		execPath, err := os.Executable()
		if err == nil {
			logDir = filepath.Dir(execPath)
		} else {
			// Fallback to current directory
			logDir = "."
		}
	}
	logFilePath := filepath.Join(logDir, "musicstate_http.log")
	fileLogger, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: Failed to open log file: %v, using stdout instead", err)
		fileLogger = nil
	} else {
		log.Printf("HTTP POST requests will be logged to: %s", logFilePath)
	}

	fileServer := http.FileServer(http.FS(staticFiles))

	// Add a dedicated endpoint for Web Scrobbler
	mux.HandleFunc("/webhook", handleHTTPPost)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Log POST requests as pretty-printed JSON
		if r.Method == http.MethodPost {
			// Read the request body
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				log.Printf("Error reading request body: %v", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
				return
			}
			// Close the original body
			r.Body.Close()

			// Create a new body reader for later use
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			// Pretty print the JSON
			var prettyJSON bytes.Buffer
			if json.Valid(bodyBytes) {
				var jsonObj interface{}
				if err := json.Unmarshal(bodyBytes, &jsonObj); err == nil {
					encoder := json.NewEncoder(&prettyJSON)
					encoder.SetIndent("", "  ")
					if err := encoder.Encode(jsonObj); err == nil {
						// Log to file if available
						if fileLogger != nil {
							timestamp := time.Now().Format("2006-01-02 15:04:05")
							logMsg := fmt.Sprintf("[%s] POST /\nFrom: %s\nData:\n%s\n",
								timestamp, r.RemoteAddr, prettyJSON.String())
							if _, err := fileLogger.WriteString(logMsg); err != nil {
								log.Printf("Error writing to log file: %v", err)
							}
						} else {
							// Log to stdout if file logging failed
							log.Printf("POST / from %s with data:\n%s", r.RemoteAddr, prettyJSON.String())
						}

						// Also process this as a Web Scrobbler request
						if scrobble, err := webscrobbler.ParseScrobble(bodyBytes); err == nil {
							log.Printf("******* WEBSCROBBLER POST TO ROOT PATH *******")
							log.Printf("EVENT: %s, IsPlaying: %v, Artist: %s, Track: %s",
								scrobble.EventName,
								scrobble.Data.Song.Parsed.IsPlaying,
								scrobble.Data.Song.Parsed.Artist,
								scrobble.Data.Song.Parsed.Track)

							// Process through the dedicated function
							if globalServer != nil {
								log.Printf("******* PROCESSING WEBSCROBBLER DATA FROM ROOT PATH *******")
								processWebScrobblerData(globalServer, &scrobble)
							}
						}
					}
				}
			} else {
				// Log non-JSON content
				if fileLogger != nil {
					timestamp := time.Now().Format("2006-01-02 15:04:05")
					logMsg := fmt.Sprintf("[%s] POST / (non-JSON)\nFrom: %s\nData: %s\n",
						timestamp, r.RemoteAddr, string(bodyBytes))
					if _, err := fileLogger.WriteString(logMsg); err != nil {
						log.Printf("Error writing to log file: %v", err)
					}
				} else {
					log.Printf("POST / from %s with non-JSON data: %s", r.RemoteAddr, string(bodyBytes))
				}
			}
		}

		// Regular static file serving
		r.URL.Path = "/static" + r.URL.Path
		fileServer.ServeHTTP(w, r)
	})

	// Handle WebSocket connections
	mux.HandleFunc("/ws", wsHandler())

	// Try alternative port if default is unavailable
	port := os.Getenv("MUSICSTATE_PORT")
	if port == "" {
		port = "5284"
	}
	hostAddr := "localhost:" + port
	fmt.Printf("SorceressEmmaly's MusicState\nCopyright (C) 2025 emmaly\nSee https://github.com/emmaly/musicstate for documentation, source code, and to file issues.\nThis program is licensed under GPLv3; it comes with ABSOLUTELY NO WARRANTY.\nThis is free software, and you are welcome to redistribute it under certain conditions.\n\nListening on http://%s\n", hostAddr)

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
	log.Printf("Starting server on %s", hostAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("Error starting server: ", err)
	}

	// Wait for server to complete shutdown
	<-serverStopCtx.Done()
	log.Println("Server shutdown complete")

	// Close the file logger if it was opened
	if fileLogger != nil {
		if err := fileLogger.Close(); err != nil {
			log.Printf("Error closing log file: %v", err)
		}
	}
}

func wsHandler() http.HandlerFunc {
	// Use the global server instance to share state across the application
	// This ensures Web Scrobbler data is properly sent to websocket clients
	server := globalServer

	// Start a connection health checker for this handler
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

		// Keep connection alive and handle incoming messages
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
		// Continue normal execution
	}

	// Lock for reading
	server.mu.RLock()

	// Check if there are any connections first
	hasConnections := len(server.connections) > 0

	// Check if there's an actual change to report
	noChange := song != nil && server.song != nil &&
		server.song.Song == song.Song &&
		server.song.Artist == song.Artist &&
		server.song.AlbumArt == song.AlbumArt

	server.mu.RUnlock()

	// If no connections, return early but still update the server state
	if !hasConnections {
		// Just update the server state if needed
		if !noChange {
			server.mu.Lock()
			server.song = song
			server.mu.Unlock()
		}
		return
	}

	// Upgrade to write lock to modify server state
	server.mu.Lock()

	// Update the stored song
	server.song = song

	// Create the appropriate update
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
		// Continue normal execution
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
		// Continue normal execution
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
	ticker := time.NewTimer(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-server.ctx.Done():
			// Context cancelled, exit the goroutine cleanly
			log.Println("Connection health checker shutting down gracefully")
			return

		case <-ticker.C:
			// Reset the timer for the next check
			ticker.Reset(healthCheckInterval)

			// First check for connections with a read lock
			server.mu.RLock()
			hasConnections := len(server.connections) > 0
			server.mu.RUnlock()

			if !hasConnections {
				continue
			}

			// Perform a health check with write lock
			server.mu.Lock()
			if len(server.connections) == 0 {
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
						// Continue with ping
					}

					// Ping the connection to keep it alive
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
				// Continue with cleanup
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

// checkWebScrobblerTimeout periodically checks for timed out Web Scrobbler data
func (server *Server) checkWebScrobblerTimeout() {
	log.Println("Starting Web Scrobbler timeout checker")

	// Create a ticker for regular checking
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-server.ctx.Done():
			// Context cancelled, exit the goroutine cleanly
			log.Println("Web Scrobbler timeout checker shutting done")
			return

		case <-ticker.C:
			now := time.Now()

			// Check if we have Web Scrobbler data
			server.mu.RLock()
			webScrobbler := server.webScrobbler
			currentSong := server.song

			// Calculate if data has timed out
			webScrobblerTimedOut := webScrobbler != nil &&
				now.Unix()-(webScrobbler.Time/1000) > int64(WebScrobblerTimeout.Seconds())

			// Check if the current song is from Web Scrobbler
			isSameArtistSong := currentSong != nil && webScrobbler != nil &&
				currentSong.Artist == webScrobbler.Data.Song.Parsed.Artist &&
				currentSong.Song == webScrobbler.Data.Song.Parsed.Track

			server.mu.RUnlock()

			// If Web Scrobbler data has timed out, clear it
			if webScrobblerTimedOut {
				timeSinceUpdate := time.Duration(now.Unix()-(webScrobbler.Time/1000)) * time.Second
				log.Printf("Web Scrobbler data timed out after %v with no updates", timeSinceUpdate)

				server.mu.Lock()
				server.webScrobbler = nil
				server.mu.Unlock()

				// Clear the song if it was from Web Scrobbler
				if isSameArtistSong {
					log.Printf("Clearing timed-out Web Scrobbler song: %s - %s",
						currentSong.Artist, currentSong.Song)
					server.reportMusic(nil)
				}
			}
		}
	}
}

// watchNightbotMusic polls the Nightbot API for current songs
func (server *Server) watchNightbotMusic() {
	log.Println("Starting Nightbot music watcher")

	// Add panic recovery
	defer func() {
		if r := recover(); r != nil {
			log.Printf("RECOVERED from panic in watchNightbotMusic: %v", r)
			// Wait a bit and restart
			time.Sleep(2 * time.Second)
			go server.watchNightbotMusic()
		}
	}()

	// Allow configuring Nightbot poll interval with environment variable
	nightbotPollSeconds, err := strconv.Atoi(os.Getenv("NIGHTBOT_POLL_INTERVAL"))
	if err != nil || nightbotPollSeconds < 1 {
		nightbotPollSeconds = int(DefaultNightbotPoll.Seconds())
	}
	nightbotPollInterval := time.Duration(nightbotPollSeconds) * time.Second
	log.Printf("Nightbot poll interval set to %v", nightbotPollInterval)

	// Create a ticker for regular polling
	ticker := time.NewTicker(nightbotPollInterval)
	defer ticker.Stop()

	// Use the server's context for cancellation
	for {
		select {
		case <-server.ctx.Done():
			// Context cancelled, exit the goroutine cleanly
			log.Println("Nightbot watcher shutting down gracefully")
			return

		case <-ticker.C:
			now := time.Now()

			// Skip if we don't have a Nightbot player configured
			if nightbotPlayer == nil {
				continue
			}

			// Poll Nightbot at the configured interval
			if now.Sub(server.lastNightbotPoll) >= nightbotPollInterval {
				log.Printf("Polling Nightbot (last poll was %v ago)", now.Sub(server.lastNightbotPoll))
				server.lastNightbotPoll = now

				// Make the API call with appropriate timeout
				_, apiCancel := context.WithTimeout(server.ctx, DefaultTimeout)
				nowPlaying, err := nightbotPlayer.GetCurrentTrack()
				apiCancel()

				if err == nil && nowPlaying != nil {
					// Convert to our Song format
					song := &Song{
						Artist:   nowPlaying.Artist,
						Song:     nowPlaying.Title,
						AlbumArt: "/images/album.jpg", // Use placeholder album art
					}

					// Store in server state and report to clients
					server.mu.Lock()
					server.nightbotSong = song
					server.mu.Unlock()

					log.Printf("Updated Nightbot song: %s - %s", song.Artist, song.Song)
					server.reportMusic(song)
				} else {
					// Clear the cached song when there's an error or no song playing
					server.mu.Lock()
					hadSong := server.nightbotSong != nil
					server.nightbotSong = nil
					server.mu.Unlock()

					if hadSong {
						log.Println("Clearing previously cached Nightbot song")
						server.reportMusic(nil)
					}

					if err != nil {
						if errors.Is(err, musicstate.ErrNoCurrentSong) {
							log.Println("No current song playing in Nightbot")
						} else {
							log.Printf("Nightbot error: %v", err)
						}
					} else {
						log.Println("No current song data from Nightbot")
					}
				}
			}
		}
	}
}

// processWebScrobblerData processes Web Scrobbler data and updates the music state
func processWebScrobblerData(server *Server, scrobble *webscrobbler.Scrobble) {
	if server == nil || scrobble == nil {
		log.Printf("Invalid server or scrobble data")
		return
	}

	log.Printf("Processing Web Scrobbler event: %s", scrobble.EventName)

	// Debug parsed song data
	log.Printf("Web Scrobbler song data: Artist=%s, Track=%s, IsPlaying=%v",
		scrobble.Data.Song.Parsed.Artist,
		scrobble.Data.Song.Parsed.Track,
		scrobble.Data.Song.Parsed.IsPlaying)

	// Create a song object regardless of state
	song := &Song{
		Artist:   scrobble.Data.Song.Parsed.Artist,
		Song:     scrobble.Data.Song.Parsed.Track,
		AlbumArt: scrobble.Data.Song.Parsed.TrackArt,
	}

	// If no album art, use the placeholder
	if song.AlbumArt == "" {
		song.AlbumArt = "/images/album.jpg"
	}

	// Lock for updating
	server.mu.Lock()

	// Store the full scrobble data
	server.webScrobbler = scrobble

	// Check if it's a playing event
	shouldShowSong := false

	// We'll consider ANY of these conditions as meaning "show the song"
	if scrobble.EventName == "nowplaying" ||
		scrobble.EventName == "resumedplaying" ||
		scrobble.EventName == "songchange" ||
		scrobble.Data.Song.Parsed.IsPlaying {

		shouldShowSong = true
		log.Printf("Considering this a 'playing' event (Will show song)")
	}

	// If we should show a song, update the state and notify clients
	if shouldShowSong {
		// Set the song directly in server state
		server.song = song
		log.Printf("Setting song state to %s - %s", song.Artist, song.Song)

		// Unlock before reporting to avoid deadlock
		server.mu.Unlock()

		// Always update all clients with the new song info
		// log.Printf("Sending song update to all websocket clients")
		server.reportMusic(song)
	} else {
		// Not a playing event, so clear the song
		// log.Printf("Not a playing event, clearing song display")
		server.song = nil

		// Unlock before reporting
		server.mu.Unlock()

		// Always notify clients to stop showing the song
		// log.Printf("Sending 'stop' to all clients")
		server.reportMusic(nil)
	}
}

// findConnection looks up a connection by its conn pointer
func (server *Server) findConnection(conn *websocket.Conn) *ConnectionState {
	server.mu.RLock()
	defer server.mu.RUnlock()

	for _, connState := range server.connections {
		if connState != nil && connState.Conn == conn {
			return connState
		}
	}
	return nil
}

// addConnection adds a connection to the server's connection pool
func (server *Server) addConnection(connState *ConnectionState) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.connections == nil {
		server.connections = make([]*ConnectionState, 0, 10) // Pre-allocate some capacity
	}

	server.connections = append(server.connections, connState)
	log.Printf("Added new connection, total connections: %d", len(server.connections))
}

// removeConnection removes a connection from the server's connection pool
func (server *Server) removeConnection(connState *ConnectionState) {
	server.mu.Lock()
	defer server.mu.Unlock()

	// Safety check for nil connections slice
	if len(server.connections) == 0 {
		return
	}

	// If we're removing the only connection, just reset the slice
	if len(server.connections) == 1 && server.connections[0] == connState {
		log.Println("Removed last connection, clearing connection list")
		server.connections = make([]*ConnectionState, 0)
		return
	}

	// Create a new slice without the connection to remove
	newConnections := make([]*ConnectionState, 0, len(server.connections)-1)

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
		log.Printf("Connection not found in list, no connection removed")
	}

	server.connections = newConnections
}

// debugLog prints debug messages only if DEBUG environment variable is set
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

// initNightbot initializes Nightbot API client if environment variables are set
func initNightbot() {
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
		// Log which variables are missing
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
}
