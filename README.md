# kismet-crowpanel

A Kismet status display for the Elecrow CrowPanel 7.0" and 5.0" HMI ESP32
displays (ESP32-S3-WROOM-1-N4R8, 800x480 RGB panel, GT911 touch).  It would probably
compile to other devices.

The display uses a Go-based Kismet client which runs alongside the Kismet
server, and a firmware for the display panel which receives the data.  The
helper application handles formatting Kismet data, fetching and formatting
OpenStreetMap tiles, and serving it all over the serial port to the display.

## Hardware

This was written and tested on an Elecrow CrowPanel 7.0", but should work
equally well on the 5" version which shares the same resolution.  There's no
reason it shouldn't work on other ESP32-S3 based devices, but I haven't tested
it.

If you're looking for the panels, here's some Amazon affiliate links which help
Kismet out:

* [Elecrow 7 inch panel](https://link.amazon/B00SSMwh0)
* [Elecrow 5 inch panel](https://link.amazon/B0cON1TXO)

## Screenshots


| | |
|---|---|
| ![Map](screenshots/map.png) | ![Device details](screenshots/map-detail.png) |
| Map: devices by type and phy, clusters, own track | A device's details |
| ![Dashboard](screenshots/dashboard.png) | ![Devices](screenshots/devices.png) |
| Dashboard | Devices, strongest first |
| ![Sources](screenshots/sources.png) | ![Alerts](screenshots/alerts.png) |
| Sources with packets/s | Alerts |
| ![Settings](screenshots/settings.png) | ![Dashboard, Amber](screenshots/dashboard-amber.png) |
| Settings | Dashboard in the Amber dark theme |
| ![Map, Amber](screenshots/map-amber.png) | ![Map, Cyberpunk](screenshots/map-cyberpunk.png) |
| Map in Amber | Map in Cyberpunk |

## Client

```sh
(cd client && go build)        # -> client/kismet-crowpanel
client/kismet-crowpanel --kismet http://localhost:2501 --apikey KEY --serial /dev/ttyUSB0
client/kismet-crowpanel --demo --serial /dev/ttyUSB0   # synthetic data, no Kismet needed
```

`--serial /dev/ttyUSB0` pushes the document to a panel on its USB-C port
(921600 baud by default, `--baud` to change); the panel's own log output
shows up in the client's log.  Nothing is served on the network unless
asked: `--listen ADDR` (port 8235, `--port`) serves the state document at
`/api/v1/state.json` for panels on Wi-Fi, and `--web` adds the browser viewer
at `/`.  The panel asks for
map tiles and sends consent over the same link (or over HTTP when it runs on
Wi-Fi).  `--user`/`--password` work instead of an API key;
`KISMET_APIKEY` and `KISMET_PASSWORD` are read from the environment.

## Map tiles

The client follows Kismet's map tile setting (`httpd_map_tile_url`,
`httpd_map_tiles` in `kismet_httpd.conf`; no URL means OpenStreetMap) and,
like Kismet's web UI, asks before using a tile server.  Tiles are fetched
only when viewed, cached in `--cache-dir` (default `~/.cache/kismet-crowpanel`,
`--tile-cache-mb` cap), and served to the panel as palette PNGs re-coloured
for each theme (Cyberpunk, for one, draws roads in neon).  `--no-map` turns
it off.

- `GET /api/v1/map.json`: tile server, mode, consent state
- `POST /api/v1/map/consent` `{"url": ..., "allow": true}`
- `GET /api/v1/tiles/<skin>/<z>/<x>/<y>.png[?cached=1]`, skin one of
  classic, amber, ocean, sunlight, red, cyberpunk (`cached=1`: from the
  cache only, 404 if not there)

The Map page follows the GPS fix north-up, zooms by speed (17 in town, 16
above 30 km/h, 15 above 70), and looks ahead along the heading when moving.
Between fixes it moves along your heading and speed, so following is
smooth.  Drag to pan, pinch (or mouse wheel in the viewer) to zoom;
following resumes 20 s later, or at once with the locate button.  When Kismet is set to ask, the page shows a consent prompt; "Not
now" can be undone from the chip at the top left.  In the web viewer, tiles
are fetched from the same client as the state document.

Devices are plotted where Kismet saw their strongest signal (or their
average location when Kismet has no peak), with Kismet's icon for their
type, coloured by phy and fading with time since last seen; ones first seen
in the last minute blink a ring.  Aircraft and vessels are drawn at the
position they report (ADS-B, AIS), turned to their heading.
Close devices cluster into numbered bubbles; tap one to zoom in, or tap a
device for its details.  The grey line is your own track.

The gear button on the map opens map options: which phys to show, and
whether to show all devices, the last hour, the last 10 minutes or only new
ones (first seen in the last minute).  The button is
outlined while a filter hides anything.  When the
view is loaded and you're moving, the panel also asks for tiles on the road
ahead, from the client's cache only: prefetching never goes to the tile
server.

- `GET /api/v1/markers?z=&lat=&lon=&w=&h=[&phy=&new=&age=]`: devices
  around a view (binary, format in `client/markers/markers.go`)
- `GET /api/v1/track`: own track (binary)
- `GET /api/v1/device/<id>.json`: one device's details

## Settings

The Settings tab (bottom of the rail) has:

- Mode: Light, Dark or Auto.  Auto switches at sunrise and sunset (worked
  out by the client from the GPS fix) or at set times on the client's clock.
- Light theme: Classic, Ocean, Sunlight (black on white, for glare).
- Dark theme: Amber, Red (only reds, map included, for night vision),
  Cyberpunk.
- Units: metric or imperial (speed, distance, map scale).
- Backlight level for light and dark.

The panel keeps settings in flash; the web viewer keeps them in the
browser.

## Web viewer

```sh
web/build.sh          # emscripten -> web/dist
client/kismet-crowpanel --demo --listen 127.0.0.1 --web
# open http://localhost:8235/
```

The viewer renders RGB565 like the panel and takes mouse input as touch.
URL options: `?page=map|dash|devices|sources|alerts|settings`, `?theme=auto|day|night`,
`?interval=ms`, `?state=URL`, `?backlight=1` (dim the canvas like the
panel's backlight).

Be sure not to leave the web viewer enabled on an insecure LAN; it's meant
for testing the configuration, and may expose your Kismet data (location, nearby
devices, etc).

## Installing the client

```sh
./configure --prefix=/usr --sysconfdir=/etc    # usual autoconf-style paths
make && sudo make install                      # make install-web for --web
sudo systemctl daemon-reload && sudo systemctl enable --now kismet-screen
```

The service runs the client with the settings in
`SYSCONFDIR/kismet-screen/kismet-screen.conf` (Kismet URL, login, panel
port), as a locked-down dynamic user in the `dialout` group.

## Firmware

Prebuilt images for both boards are in `firmware/prebuilt/` (USB serial
link at 921600 baud).  The client flashes them itself, no PlatformIO or
esptool needed:

```sh
client/kismet-crowpanel flash --port /dev/ttyUSB0
client/kismet-crowpanel flash --port /dev/ttyUSB0 --board crowpanel5   # first install
```

With no `--board` it restarts the panel and asks the firmware already on it
which board it is (trying the link at 921600, then 460800 baud, or
`--link-baud`).  That needs this project's firmware; the first time, over
Elecrow's demo, give `--board crowpanel7` or `--board crowpanel5`.
`--dry-run` shows what it would flash.

The board is reset into its ROM loader through the USB port's DTR/RTS
lines; if that fails, hold BOOT while pressing RESET.  `--image FILE` (and
`--address 0x10000` for an app image alone) flashes your own build;
`--baud` sets the flashing speed (460800 by default).

To build your own:

```sh
cp firmware/include/widget_config.example.h firmware/include/widget_config.h
# link = transport::serial by default; transport::wifi needs the SSID and state URL
firmware/build.sh --net     # first build fetches the toolchain; later builds offline
(cd firmware && pio run -e crowpanel5)   # the 5.0" board
firmware/prebuild.sh        # the images in firmware/prebuilt
```

Pins and panel timings follow Elecrow's examples for each board.  On v3.0
boards the touch controller is reset through a PCA9557; the firmware detects
that at startup.  On macOS the CH340 USB bridge garbles 921600 baud: build
with `serial_baud = 460800` and run the client with `--baud 460800`.


## License

GPL v2 or (at your option) any later version; see `LICENSE`.  The device
icons are drawn from Kismet's, also GPL.
