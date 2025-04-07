# SorceressEmmaly's MusicState - a music visualization overlay for streams

A sleek music visualization overlay for your streams that shows what's currently playing in TIDAL or Spotify.

## Features

- Shows current song and artist information from TIDAL, Spotify, or Nightbot's song request system
- Smooth animations for song changes
- Customizable appearance including:
  - Position on screen (9 different anchor points)
  - Colors
  - Size
  - Animation speed
  - Background opacity
  - Mini mode for a more compact display
- Works as a browser source in OBS, Streamlabs, etc.
- No login or authentication required - works by detecting currently playing songs locally

## Setup

1. Download and run the application
2. It will start a local web server and display the URL to use (typically `http://localhost:52846`)
3. Add this URL as a browser source in your streaming software
4. Play music in TIDAL, Spotify, or Nightbot
5. The overlay will automatically detect and display currently playing songs

If port 52846 is already in use or blocked, you can specify an alternative port by setting the `MUSICSTATE_PORT` environment variable:

```
# Windows PowerShell
$env:MUSICSTATE_PORT = "8080"
.\musicstate.exe

# Windows Command Prompt
set MUSICSTATE_PORT=8080
musicstate.exe
```

You can also configure how frequently the application polls Nightbot's API (default is 5 seconds):

```
# Windows PowerShell
$env:NIGHTBOT_POLL_INTERVAL = "10"  # Poll every 10 seconds
.\musicstate.exe

# Windows Command Prompt
set NIGHTBOT_POLL_INTERVAL=10
musicstate.exe
```

## Customization

You can customize the appearance by adding URL parameters:

- `origin`: Position anchor (default: `bl`)
  - `tl`: Top left
  - `tc`: Top center
  - `tr`: Top right
  - `cl`: Center left
  - `cc`: Center
  - `cr`: Center right
  - `bl`: Bottom left
  - `bc`: Bottom center
  - `br`: Bottom right
- `size`: Font size in pixels (default: `36`)
- `color`: Text color (default: `white`)
- `bg`: Background color/opacity (default: `rgba(0,0,0,0.5)`)
- `speed`: Animation speed in seconds (default: `0.6`)
- `mini`: Scale factor for mini mode (default: `0.6`)
- `dwell`: How long to show full size before mini mode in seconds (default: `3`)

Example URL with parameters:

```text
http://localhost:52846?origin=tr&size=42&color=yellow&bg=rgba(0,0,0,0.8)&speed=0.8&mini=0.5&dwell=5
```

## Requirements

- Windows OS _(for now)_
- TIDAL or Spotify desktop application, or Nightbot access
- Web browser or streaming software that supports browser sources

## Nightbot Integration

MusicState now supports Nightbot's song request system via their API. To set this up:

1. Create a Nightbot application at https://nightbot.tv/account/applications
2. Set the redirect URL to a local URL like `http://localhost:9182` (make sure to include the `http://` prefix)
3. Build and run the token generator:
   ```
   cd cmd/tokengenerator
   go build
   ./tokengenerator <client_id> <client_secret> "http://localhost:9182"
   ```
   
   **Important**: The redirect URL must match exactly what you configured in the Nightbot application and be enclosed in quotes
4. Follow the instructions to authorize the application
5. Set the environment variables provided by the tool:
   ```
   # Windows PowerShell
   $env:NIGHTBOT_CLIENT_ID = "your_client_id"
   $env:NIGHTBOT_CLIENT_SECRET = "your_client_secret"
   $env:NIGHTBOT_REDIRECT_URL = "your_redirect_url" 
   $env:NIGHTBOT_TOKEN = '{"access_token":"...","token_type":"bearer","refresh_token":"...","expiry":"..."}'
   
   # Windows Command Prompt
   set NIGHTBOT_CLIENT_ID=your_client_id
   set NIGHTBOT_CLIENT_SECRET=your_client_secret
   set NIGHTBOT_REDIRECT_URL=your_redirect_url
   set NIGHTBOT_TOKEN={"access_token":"...","token_type":"bearer","refresh_token":"...","expiry":"..."}
   ```
6. Run MusicState with these environment variables set

The application will automatically detect and show songs from Nightbot when available, falling back to TIDAL and Spotify when no song is playing in Nightbot.

## Building from Source

1. Ensure you have Go installed
2. Clone the repository
3. Run `GOOS=windows go build` (if building from WSL or Linux)

## Limitations

- Currently only supports Windows _(for now)_
- Desktop player support: Works with TIDAL and Spotify desktop applications
- API support: Works with Nightbot's song request system
- For desktop players: Requires the applications to be running and playing music
- For Nightbot: Requires OAuth2 authentication (see Nightbot integration docs)

## Contributing

Contributions are welcome! Please feel free to submit pull requests.

## License

This project is open source and available under the GNU General Public License v3 (GPL-3.0). This means:

- Anyone can view, use, modify, and distribute the code
- If you distribute modified versions, you must:
  - Make your source code available
  - License it under GPL-3.0
  - Document your changes
- If this code is used in a larger software project, that project must also be GPL-3.0 compliant
- No additional restrictions can be placed on recipients of the code

This license is specifically chosen to ensure that improvements to the code remain available to the community.
