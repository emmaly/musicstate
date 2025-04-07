# MusicState

A simple web overlay that displays the currently playing song from Web Scrobbler (browser extension) or Nightbot (song requests).

## Features

- Receives track information from Web Scrobbler browser extension via webhook
- Integrates with Nightbot's song request system via API
- Displays current song information via a web interface (artist, title, album art)
- Designed for streaming overlays with configurable appearance
- Automatically hides when music stops playing
- Works with any browser or OBS browser source

## Building

```bash
go build
```

## Usage

1. Run the executable. By default, it will listen on `localhost:5284`
2. Add `http://localhost:5284` as a browser source in OBS or open in a browser
3. Configure either Web Scrobbler or Nightbot integration (or both):
   - **Web Scrobbler**: 
     - Install the Web Scrobbler browser extension
     - In Web Scrobbler, go to the "Accounts" section and add a Webhook entry with the API URL value of `http://localhost:5284/webhook`
     - Play music through any service supported by Web Scrobbler
   - **Nightbot**: Set up the required environment variables (see Nightbot Integration section)

### Configuration Options

The overlay appearance can be customized by adding URL parameters:

- `origin`: Position of the display (default: `bl`)
  - `tl`: Top Left
  - `tr`: Top Right
  - `bl`: Bottom Left
  - `br`: Bottom Right
  - `cc`: Center
  - `tc`: Top Center
  - `bc`: Bottom Center
  - `cl`: Center Left
  - `cr`: Center Right
- `size`: Font size in pixels (default: `36`)
- `color`: Text color (default: `white`)
- `bg`: Background color (default: `rgba(0,0,0,0.5)`)
- `dwell`: How long to show full size before minimizing, in seconds (default: `3`)
- `mini`: Scale factor when minimized (default: `0.6`)

Example: `http://localhost:5284?bg=rgba(0,0,0,0.8)&color=red&size=24`

## Environment Variables

- `MUSICSTATE_PORT`: Port to listen on (default: 5284)
- `MUSICSTATE_LOG_DIR`: Directory to store logs (default: executable directory)

### Nightbot Integration

To enable Nightbot integration, set the following environment variables:

- `NIGHTBOT_CLIENT_ID`: OAuth client ID for Nightbot API
- `NIGHTBOT_CLIENT_SECRET`: OAuth client secret for Nightbot API  
- `NIGHTBOT_REDIRECT_URL`: OAuth redirect URL
- `NIGHTBOT_TOKEN`: JSON-formatted OAuth token for Nightbot API
- `NIGHTBOT_POLL_INTERVAL`: Polling interval in seconds (default: 5)

#### Obtaining Nightbot Credentials

1. Create a Nightbot application at [https://api.nightbot.tv/oauth2/applications](https://api.nightbot.tv/oauth2/applications)
2. Set the redirect URL to a valid URL for your application (or use `http://localhost` for testing)
3. Get your client ID and client secret from the application details
4. Use the OAuth 2.0 flow to obtain an access token with the `song_requests_queue` scope
5. Save the token response JSON as the value for `NIGHTBOT_TOKEN`

When properly configured, MusicState will poll the Nightbot API to get currently playing song request information and display it in the overlay.

## License

This project is licensed under the GPLv3 License - see the LICENSE file for details.
