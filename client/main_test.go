package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kismet-crowpanel/tiles"
)

func get(t *testing.T, url string) (int, []byte) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b
}

func TestHTTP(t *testing.T) {
	var tile bytes.Buffer
	png.Encode(&tile, image.NewGray(image.Rect(0, 0, 256, 256)))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tile.Bytes())
	}))
	defer upstream.Close()
	tileURL := upstream.URL + "/http/{z}/{x}/{y}.png"

	st := newState("demo")
	p := newDemoPoller(st)
	p.poll()
	ts, err := tiles.New(t.TempDir(), 1<<20, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ts.SetSettings(map[string]any{"url": tileURL, "tiles": "ask"})
	st.tiles = ts
	srv := httptest.NewServer(handler(st, t.TempDir()))
	defer srv.Close()

	var m tiles.Doc
	_, b := get(t, srv.URL+"/api/v1/map.json")
	if json.Unmarshal(b, &m) != nil || m.Consent != "pending" || m.URL != tileURL {
		t.Errorf("map.json reports pending consent: %s", b)
	}
	var doc stateDoc
	_, b = get(t, srv.URL+"/api/v1/state.json")
	if json.Unmarshal(b, &doc) != nil || doc.Map == nil || doc.Map.Consent != "pending" || doc.Map.URL != tileURL ||
		doc.Link != "ok" || len(doc.Sources) != 4 || doc.GPS.Fix != 3 {
		t.Errorf("state document carries the demo and the map status: %s", b)
	}
	if code, _ := get(t, srv.URL+"/api/v1/tiles/day/2/1/1.png"); code != 403 {
		t.Errorf("tile refused before consent: %d", code)
	}
	body, _ := json.Marshal(map[string]any{"url": tileURL, "allow": true})
	r, err := http.Post(srv.URL+"/api/v1/map/consent", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(r.Body).Decode(&m)
	r.Body.Close()
	if !m.Available {
		t.Error("consent POST enables tiles")
	}
	if code, b := get(t, srv.URL+"/api/v1/tiles/night/2/1/1.png"); code != 200 || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Errorf("night tile served: %d", code)
	}
	if code, _ := get(t, srv.URL+"/api/v1/tiles/day/2/1/2.png?cached=1"); code != 404 {
		t.Errorf("uncached prefetch refused: %d", code)
	}
	if code, b := get(t, srv.URL+"/api/v1/tiles/cyberpunk/2/1/1.png"); code != 200 || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Errorf("cyberpunk tile served: %d", code)
	}
	if code, _ := get(t, srv.URL+"/api/v1/tiles/dusk/2/1/1.png"); code != 400 {
		t.Errorf("unknown skin refused: %d", code)
	}

	code, b := get(t, srv.URL+"/api/v1/markers?z=15&lat=51.4769&lon=-0.0005&w=736&h=448")
	if code != 200 || len(b) < 6 || int(b[4])|int(b[5])<<8 == 0 {
		t.Errorf("markers around the demo drive: %d, %d bytes", code, len(b))
	}
	if code, _ := get(t, srv.URL+"/api/v1/markers?z=15"); code != 400 {
		t.Errorf("markers need a full view: %d", code)
	}
	if code, b := get(t, srv.URL+"/api/v1/device/1.json"); code != 200 || !bytes.Contains(b, []byte(`"mac":"`)) {
		t.Errorf("device detail: %d %s", code, b)
	}
	if code, _ := get(t, srv.URL+"/api/v1/device/99999.json"); code != 404 {
		t.Errorf("unknown device: %d", code)
	}
	if code, b := get(t, srv.URL+"/api/v1/track"); code != 200 || len(b) < 6 {
		t.Errorf("track: %d", code)
	}
}

func TestHelpers(t *testing.T) {
	if channelNumber("36HT40+") != 36 || channelNumber("") != 0 || channelNumber("abc") != 0 {
		t.Error("leading channel numbers")
	}
	if clip("abcdef", 4) != "ab~" || clip("abc", 4) != "abc" {
		t.Error("clip")
	}
	// midsummer noon in London is day, midnight is night
	noon := float64(time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC).Unix())
	if sunElevation(51.48, 0, noon) < 55 || sunElevation(51.48, 0, noon+12*3600) > -10 {
		t.Error("sun elevation")
	}
}

func TestViewerOff(t *testing.T) {
	st := newState("test")
	srv := httptest.NewServer(handler(st, ""))
	defer srv.Close()
	if code, _ := get(t, srv.URL+"/"); code != 404 {
		t.Errorf("no viewer pages without a web dir: %d", code)
	}
	if code, _ := get(t, srv.URL+"/api/v1/state.json"); code != 200 {
		t.Errorf("the panel API is still served: %d", code)
	}
}

func TestCheckLinks(t *testing.T) {
	if checkLinks("/dev/ttyUSB0", "", false) != nil {
		t.Error("serial alone is enough")
	}
	if checkLinks("", "127.0.0.1", true) != nil {
		t.Error("HTTP alone is enough")
	}
	if checkLinks("", "", false) == nil {
		t.Error("neither serial nor HTTP is refused")
	}
	if checkLinks("/dev/ttyUSB0", "", true) == nil {
		t.Error("--web without --listen is refused")
	}
}
