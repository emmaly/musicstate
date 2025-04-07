# Nightbot Integration

This package provides integration with Nightbot's song request system via their API.

## Setup

1. Create a Nightbot application at https://nightbot.tv/account/applications
2. Set the redirect URL to a local URL like `http://localhost:9182` (make sure to include the `http://` prefix)
3. Note your Client ID and Client Secret
4. Run the token generator:

```bash
go run ./cmd/tokengenerator/tokengenerator.go <client_id> <client_secret> "http://localhost:9182"
```

**Important**: The redirect URL must:
- Start with `http://`
- Match exactly what you configured in the Nightbot application
- Use a port that isn't in use on your system
- Be enclosed in quotes when using the token generator

5. Visit the URL provided to authorize the application
6. After authorization, the tool will display environment variables to set
7. Set these environment variables before running the main application:

```bash
export NIGHTBOT_CLIENT_ID=your_client_id
export NIGHTBOT_CLIENT_SECRET=your_client_secret
export NIGHTBOT_REDIRECT_URL=your_redirect_url
export NIGHTBOT_TOKEN='{"access_token":"...","token_type":"bearer","refresh_token":"...","expiry":"..."}'
```

## Usage

Once the environment variables are set, the application will automatically use Nightbot as a music source. It will retrieve the current song information from Nightbot's API every second.

## Notes

- The token includes a refresh token which will be used automatically when the access token expires
- The application will check both Nightbot and window-based players (TIDAL, Spotify), using the first one that returns a current track
- By default, Nightbot is polled every 5 seconds to reduce API usage
- You can adjust the polling interval with the `NIGHTBOT_POLL_INTERVAL` environment variable (in seconds):
  ```bash
  export NIGHTBOT_POLL_INTERVAL=10  # Poll Nightbot every 10 seconds
  ```
