package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"

	"kismet-crowpanel/markers"
	"kismet-crowpanel/tiles"
)

var (
	tilePath   = regexp.MustCompile(`^/api/v1/tiles/([a-z]+)/(\d+)/(\d+)/(\d+)\.png$`)
	devicePath = regexp.MustCompile(`^/api/v1/device/(\d+)\.json$`)
)

func reply(w http.ResponseWriter, code int, body []byte, ctype, cache string) {
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("Cache-Control", cache)
	h.Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	w.Write(body)
}

func replyJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	reply(w, code, b, "application/json", "no-store")
}

func replyErr(w http.ResponseWriter, code int, msg string) {
	replyJSON(w, code, map[string]string{"error": msg})
}

func tileErr(w http.ResponseWriter, err error) {
	var te *tiles.Error
	if errors.As(err, &te) {
		replyErr(w, te.Status, te.Reason)
		return
	}
	replyErr(w, 500, err.Error())
}

// handler serves the API, and the web viewer from webDir unless it's empty
func handler(st *state, webDir string) http.Handler {
	files := http.NotFoundHandler()
	if webDir != "" {
		files = http.FileServer(http.Dir(webDir))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if r.Method == http.MethodPost {
			if path != "/api/v1/map/consent" || st.tiles == nil {
				replyErr(w, 404, "not found")
				return
			}
			var req struct {
				URL   *string `json:"url"`
				Allow *bool   `json:"allow"`
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
			if err == nil && len(body) > 4096 {
				err = errors.New("too large")
			}
			if err == nil {
				err = json.Unmarshal(body, &req)
			}
			if err == nil && (req.URL == nil || req.Allow == nil) {
				err = errors.New("need url and allow")
			}
			if err != nil {
				replyErr(w, 400, err.Error())
				return
			}
			if err := st.tiles.SetConsent(*req.URL, *req.Allow); err != nil {
				tileErr(w, err)
				return
			}
			replyJSON(w, 200, st.tiles.Doc())
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			replyErr(w, 405, "method not allowed")
			return
		}

		switch path {
		case "/api/v1/state.json":
			reply(w, 200, st.snapshot(), "application/json", "no-store")
			return
		case "/api/v1/map.json":
			replyJSON(w, 200, st.mapDoc())
			return
		case "/api/v1/markers":
			q := r.URL.Query()
			v, err := markers.ViewFrom(func(k string) (string, bool) {
				s, ok := q[k]
				if !ok || len(s) == 0 {
					return "", false
				}
				return s[0], true
			})
			if err != nil {
				replyErr(w, 400, "need z, lat, lon, w, h")
				return
			}
			reply(w, 200, st.devmap.Markers(v), "application/octet-stream", "no-store")
			return
		case "/api/v1/track":
			_, b := st.trail.Payload()
			reply(w, 200, b, "application/octet-stream", "no-store")
			return
		}
		if m := devicePath.FindStringSubmatch(path); m != nil {
			id, _ := strconv.ParseUint(m[1], 10, 32)
			if d, ok := st.devmap.Detail(uint32(id)); ok {
				replyJSON(w, 200, d)
			} else {
				replyErr(w, 404, "no such device")
			}
			return
		}
		if m := tilePath.FindStringSubmatch(path); m != nil {
			if st.tiles == nil {
				replyErr(w, 404, "map disabled")
				return
			}
			z, _ := strconv.Atoi(m[2])
			x, _ := strconv.Atoi(m[3])
			y, _ := strconv.Atoi(m[4])
			cached := r.URL.Query().Get("cached") == "1"
			var png []byte
			var err error
			if rot, _ := strconv.Atoi(r.URL.Query().Get("rot")); rot != 0 {
				png, err = st.tiles.GetTurned(m[1], z, x, y, rot, cached)
			} else {
				png, err = st.tiles.Get(m[1], z, x, y, cached)
			}
			if err != nil {
				tileErr(w, err)
				return
			}
			reply(w, 200, png, "image/png", "max-age=3600")
			return
		}
		debugf("%s %s", r.RemoteAddr, r.URL)
		files.ServeHTTP(w, r)
	})
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if verbose {
			log.Printf("DEBUG %s %s %s", r.RemoteAddr, r.Method, r.URL)
		}
		h.ServeHTTP(w, r)
	})
}
