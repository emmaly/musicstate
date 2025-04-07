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

type Server struct {
	song        *Song
	connections []*websocket.Conn
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

	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("Upgrade error:", err)
			return
		}
		defer conn.Close()

		server.addConnection(conn)
		defer server.removeConnection(conn)

		// Send the current state to the new connection
		var update SongUpdate
		if server.song != nil {
			update = server.song.AsSongUpdate()
			log.Printf("Sending current song to new connection: %s - %s", server.song.Artist, server.song.Song)
		} else {
			update = SongUpdate{Type: "stop"}
			log.Println("Sending stop state to new connection (no song playing)")
		}
		
		err = conn.WriteJSON(update)
		if err != nil {
			log.Println("Write error:", err)
			return
		}

		// Keep connection alive and handle any incoming messages
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				log.Println("Read error:", err)
				break
			}
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
	defer server.mu.Unlock()

	if (server.song == nil && song == nil) ||
		(server.song != nil &&
			song != nil &&
			server.song.Song == song.Song &&
			server.song.Artist == song.Artist &&
			server.song.AlbumArt == song.AlbumArt) {
		return // nothing to do
	}

	server.song = song

	if server.connections == nil {
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

	// Send to all connected clients
	for _, conn := range server.connections {
		err := conn.WriteJSON(update)
		if err != nil {
			log.Println("Write error:", err)
		}
	}
}

func (server *Server) addConnection(conn *websocket.Conn) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.connections == nil {
		server.connections = make([]*websocket.Conn, 0)
	}

	server.connections = append(server.connections, conn)
}

func (server *Server) removeConnection(conn *websocket.Conn) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if server.connections == nil {
		return
	}

	for i, c := range server.connections {
		if c == conn {
			server.connections = append(server.connections[:i], server.connections[i+1:]...)
			break
		}
	}
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
					if errors.Is(err, errors.New("no current song playing")) {
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
