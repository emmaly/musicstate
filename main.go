package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emmaly/musicstate/winapi"
	"github.com/gorilla/websocket"
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

	hostAddr := "localhost:52846"
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

		if server.song != nil {
			// Send the current song to the new connection
			err = conn.WriteJSON(server.song.AsSongUpdate())
			if err != nil {
				log.Println("Write error:", err)
				return
			}
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

	update := song.AsSongUpdate()

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
		tidal   = "TIDAL.exe"
		spotify = "Spotify.exe"
	)

	for {
		windows, err := winapi.FindWindowsByProcess(
			[]string{tidal, spotify},
			winapi.WinVisible(true),
			winapi.WinClass("Chrome_WidgetWin_1"),
			winapi.WinTitlePattern(*regexp.MustCompile("-")),
		)
		if err != nil {
			log.Fatal(err)
		}

		var song *Song

		for _, w := range windows {
			delimiter := " - "
			songParts := strings.Split(w.Title, delimiter)
			if len(songParts) >= 2 {
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
			if song != nil {
				break
			}
		}

		if song != nil && song.Song != "" && song.AlbumArt == "" {
			// set the placeholder album art
			song.AlbumArt = "http://localhost:52846/images/album.jpg"
		}

		server.reportMusic(song)

		time.Sleep(1 * time.Second)
	}
}
