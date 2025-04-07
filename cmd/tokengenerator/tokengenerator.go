package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Printf("Usage: %s <client_id> <client_secret> <redirect_url>\n", os.Args[0])
		os.Exit(1)
	}

	clientID := os.Args[1]
	clientSecret := os.Args[2]
	redirectURL := os.Args[3]

	// Configure OAuth2
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

	// Generate auth URL
	authURL := config.AuthCodeURL("state", oauth2.AccessTypeOffline)
	fmt.Printf("Visit the URL for the auth dialog: %v\n", authURL)

	// Start local server to receive callback
	var token *oauth2.Token
	tokenChan := make(chan *oauth2.Token)
	
	// Parse the redirect URL to get host and port
	redirectParts := strings.Split(strings.TrimPrefix(redirectURL, "http://"), ":")
	if len(redirectParts) != 2 {
		log.Fatalf("Invalid redirect URL format. Should be http://hostname:port (example: http://localhost:9182)")
	}
	listenAddr := ":" + redirectParts[1]
	
	// Create a server to listen for the OAuth2 callback
	server := &http.Server{Addr: listenAddr}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Code not found", http.StatusBadRequest)
			return
		}

		// Exchange the code for a token
		token, err := config.Exchange(r.Context(), code)
		if err != nil {
			http.Error(w, fmt.Sprintf("Token exchange error: %v", err), http.StatusInternalServerError)
			return
		}

		// Send the token to the main goroutine
		tokenChan <- token

		// Return success to the browser
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<h1>Authentication Successful!</h1><p>You can close this window now.</p>")
	})

	// Start the server in a goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for the token
	token = <-tokenChan

	// Shut down the server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(ctx)

	// Output the token as JSON
	tokenJSON, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling token: %v", err)
	}

	fmt.Println("\nToken received! Please set the following environment variables:")
	fmt.Printf("NIGHTBOT_CLIENT_ID=%s\n", clientID)
	fmt.Printf("NIGHTBOT_CLIENT_SECRET=%s\n", clientSecret)
	fmt.Printf("NIGHTBOT_REDIRECT_URL=%s\n", redirectURL)
	fmt.Printf("NIGHTBOT_TOKEN=%s\n", string(tokenJSON))
}