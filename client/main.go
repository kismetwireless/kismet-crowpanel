// Kismet CrowPanel client.
//
// Polls a Kismet server's REST API, condenses it into a small state
// document the ESP32 display can parse in a few KB, and serves it over HTTP
// at /api/v1/state.json.  Also serves the web viewer (web/dist) at /.
//
// With --serial, the document is also pushed to the panel over its USB
// serial port, framed as in ui/link_frame.h.  "kismet-crowpanel flash"
// writes a panel's firmware over that port (flash.go).
//
// Map tiles (package tiles) follow Kismet's map tile setting and consent
// and are served at /api/v1/tiles/<day|night>/<z>/<x>/<y>.png.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"kismet-crowpanel/tiles"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "flash" {
		os.Exit(flashMain(os.Args[2:]))
	}
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		home, _ := os.UserHomeDir()
		cacheHome = filepath.Join(home, ".cache")
	}
	webDefault := "web/dist"
	if exe, err := os.Executable(); err == nil {
		// the binary normally sits in client/
		webDefault = filepath.Join(filepath.Dir(exe), "..", "web", "dist")
	}
	// installed by make install-web
	if web := filepath.Join(dataDir, "web"); dataDir != "" {
		if _, err := os.Stat(filepath.Join(web, "index.html")); err == nil {
			webDefault = web
		}
	}

	// the systemd unit's settings come in as environment variables
	kismetURL := flag.String("kismet", firstOf(os.Getenv("KISMET_URL"), "http://localhost:2501"),
		"Kismet server URL (or $KISMET_URL)")
	apikey := flag.String("apikey", os.Getenv("KISMET_APIKEY"), "Kismet API key (or $KISMET_APIKEY)")
	user := flag.String("user", os.Getenv("KISMET_USER"), "Kismet username, if not using an API key (or $KISMET_USER)")
	password := flag.String("password", os.Getenv("KISMET_PASSWORD"), "Kismet password (or $KISMET_PASSWORD)")
	name := flag.String("name", "", "server name to show when Kismet doesn't report one")
	listen := flag.String("listen", "", "serve HTTP on this address, for Wi-Fi panels and --web (off by default)")
	port := flag.Int("port", 8235, "HTTP port, with --listen")
	interval := flag.Float64("interval", 2.0, "Kismet poll interval, seconds")
	web := flag.Bool("web", false, "also serve the browser viewer (the API for Wi-Fi panels is always served)")
	webDir := flag.String("web-dir", webDefault, "directory with the built web viewer, with --web")
	demo := flag.Bool("demo", false, "serve synthetic data instead of polling Kismet")
	serial := flag.String("serial", os.Getenv("CROWPANEL_SERIAL"),
		"push state to a panel on this serial `port`, or \"auto\" to find it on a CH340 USB port (or $CROWPANEL_SERIAL)")
	cacheDir := flag.String("cache-dir", filepath.Join(cacheHome, "kismet-crowpanel"),
		"map tile cache and consent")
	tileCacheMB := flag.Int64("tile-cache-mb", 500, "map tile cache size cap")
	noMap := flag.Bool("no-map", false, "don't offer map tiles at all")
	demoMapURL := flag.String("demo-map-url", "",
		"with --demo: tile URL template standing in for Kismet's setting (default OSM)")
	demoMapTiles := flag.String("demo-map-tiles", "ask", "with --demo: Kismet's httpd_map_tiles stand-in (ask|deny)")
	baudDefault, err := envInt("CROWPANEL_BAUD", 921600)
	if err != nil {
		log.Fatalf("%v", err)
	}
	baud := flag.Int("baud", baudDefault, "serial link rate to move the panel to from its starting 115200, or\n"+
		"the fastest slower one that works (or $CROWPANEL_BAUD)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.BoolVar(&verbose, "verbose", false, "verbose logging")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: kismet-crowpanel [options]\n"+
			"       kismet-crowpanel flash --port PORT  (flash a panel's firmware; flash -h for more)\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if *demoMapTiles != "ask" && *demoMapTiles != "deny" {
		log.Fatalf("--demo-map-tiles must be ask or deny")
	}
	if *serial != "" && !baudOK(*baud) {
		log.Fatalf("unsupported baud rate %d", *baud)
	}
	if err := checkLinks(*serial, *listen, *web); err != nil {
		log.Fatalf("%v", err)
	}

	var st *state
	var p poller
	if *demo {
		st = newState(firstOf(*name, "demo"))
		p = newDemoPoller(st)
	} else {
		host := "kismet"
		if u, err := url.Parse(*kismetURL); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		st = newState(firstOf(*name, host))
		p = newKismetPoller(st, newKismetAPI(*kismetURL, *apikey, *user, *password))
	}

	if !*noMap {
		ts, err := tiles.New(*cacheDir, *tileCacheMB<<20, tiles.FetchTimeout)
		if err != nil {
			log.Fatalf("tile cache: %v", err)
		}
		st.tiles = ts
		if *demo {
			ts.SetSettings(map[string]any{"url": *demoMapURL, "tiles": *demoMapTiles})
		}
	}

	every := time.Duration(*interval * float64(time.Second))
	go st.run(p, every)
	if *serial != "" {
		l := &serialLink{st: st, path: *serial, baud: *baud, interval: every}
		go l.run()
	}

	if *listen == "" {
		select {}
	}

	dir := ""
	if *web {
		dir, _ = filepath.Abs(*webDir)
		if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
			log.Printf("WARNING no web viewer in %s; run web/build.sh (state API still served)", dir)
		}
	}

	addr := net.JoinHostPort(*listen, strconv.Itoa(*port))
	if *web {
		log.Printf("INFO serving the viewer on http://%s/ (state at /api/v1/state.json)", addr)
	} else {
		log.Printf("INFO serving the panel API on http://%s/api/v1/ (viewer off; --web turns it on)", addr)
	}
	srv := &http.Server{Addr: addr, Handler: logRequests(handler(st, dir)), ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func envInt(name string, def int) (int, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("$%s: %q isn't a number", name, v)
	}
	return n, nil
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// checkLinks wants at least one way to reach a panel: serial, or HTTP for
// Wi-Fi panels and the viewer
func checkLinks(serial, listen string, web bool) error {
	if web && listen == "" {
		return errors.New("--web needs --listen")
	}
	if serial == "" && listen == "" {
		return errors.New("nothing to serve: give --serial for a panel on USB, or --listen for Wi-Fi panels")
	}
	return nil
}
