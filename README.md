# SorceressEmmaly's MusicState - a music visualization overlay for streams

A sleek music visualization overlay for your streams that shows what's currently playing in TIDAL or Spotify.

## Features

- Shows current song and artist information from TIDAL or Spotify
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
4. Play music in either TIDAL or Spotify desktop applications
5. The overlay will automatically detect and display currently playing songs

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
- TIDAL or Spotify desktop application
- Web browser or streaming software that supports browser sources

## Building from Source

1. Ensure you have Go installed
2. Clone the repository
3. Run `go build`

## Limitations

- Currently only supports Windows _(for now)_
- Works with TIDAL and Spotify desktop applications only (not web players)
- Requires the desktop applications to be running and playing music

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
