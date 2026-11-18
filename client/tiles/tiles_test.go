package tiles

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// SamplePNG is light land, a white road, a blue lake and dark text-ish
// marks, like an OSM tile.
func samplePNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	set := func(x0, y0, x1, y1, step int, c color.RGBA) {
		for x := x0; x < x1; x += step {
			for y := y0; y < y1; y++ {
				img.Set(x, y, c)
			}
		}
	}
	set(0, 0, 256, 256, 1, color.RGBA{242, 239, 233, 255})
	set(0, 120, 256, 128, 1, color.RGBA{255, 255, 255, 255})
	set(30, 30, 90, 80, 1, color.RGBA{170, 211, 223, 255})
	set(150, 60, 200, 70, 3, color.RGBA{40, 40, 40, 255})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

type mock struct {
	srv       *httptest.Server
	png       []byte
	mu        sync.Mutex
	requests  []http.Header
	mode      string
	delay     time.Duration
	active    int
	maxActive int
}

func newMock() *mock {
	m := &mock{png: samplePNG(), mode: "ok"}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.requests = append(m.requests, r.Header.Clone())
		m.active++
		m.maxActive = max(m.maxActive, m.active)
		mode, delay := m.mode, m.delay
		m.mu.Unlock()
		defer func() { m.mu.Lock(); m.active--; m.mu.Unlock() }()
		time.Sleep(delay)
		switch {
		case mode == "429":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
		case mode == "down":
			w.WriteHeader(503)
		case strings.HasPrefix(r.URL.Path, "/404/"):
			w.WriteHeader(404)
		case r.Header.Get("If-None-Match") == `"v1"`:
			w.Header().Set("Cache-Control", "max-age=100, stale-while-revalidate=1000")
			w.WriteHeader(304)
		default:
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "max-age=100, stale-while-revalidate=1000")
			w.Header().Set("ETag", `"v1"`)
			w.Write(m.png)
		}
	}))
	return m
}

func (m *mock) url(prefix string) string { return m.srv.URL + prefix + "/{z}/{x}/{y}.png" }

func (m *mock) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *mock) set(mode string, delay time.Duration) {
	m.mu.Lock()
	m.mode, m.delay = mode, delay
	m.mu.Unlock()
}

func waitFor(pred func() bool) bool {
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if pred() {
			return true
		}
	}
	return false
}

type clock struct {
	mu sync.Mutex
	t  float64
}

func (c *clock) now() float64  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d float64) { c.mu.Lock(); c.t += d; c.mu.Unlock() }

func service(t *testing.T, dir string, c *clock, url string) *Service {
	t.Helper()
	s, err := New(dir, 500<<20, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = c.now
	s.SetSettings(map[string]any{"url": url, "tiles": "ask"})
	return s
}

func status(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func rgbAt(t *testing.T, b []byte, x, y int) (int, int, int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r, g, bl, _ := img.At(x, y).RGBA()
	return int(r >> 8), int(g >> 8), int(bl >> 8)
}

func TestSettings(t *testing.T) {
	d := FromKismet(map[string]any{"url": "", "tiles": "ask", "attribution": ""})
	if d.URL != OSMURL || !strings.Contains(d.Attribution, "OpenStreetMap") {
		t.Error("empty Kismet URL means OSM with attribution")
	}
	d = FromKismet(map[string]any{"url": "https://t.example/{z}/{x}/{y}.png", "tiles": "bogus"})
	if d.Tiles != "ask" || d.Attribution != "" {
		t.Error("custom URL keeps its own (empty) attribution; bad mode -> ask")
	}
}

func TestService(t *testing.T) {
	m := newMock()
	defer m.srv.Close()
	dir := t.TempDir()
	c := &clock{t: 1_000_000}
	s := service(t, dir, c, m.url(""))

	if d := s.Doc(); d.Available || d.Consent != "pending" || d.Host != "127.0.0.1" {
		t.Errorf("ask mode starts pending consent: %+v", d)
	}
	if _, err := s.Get("day", 1, 0, 0, false); status(err) != 403 || m.count() != 0 {
		t.Error("tiles refused before consent, no upstream request")
	}
	if err := s.SetConsent("http://other/{z}/{x}/{y}.png", true); status(err) != 409 {
		t.Error("consent for a stale URL rejected")
	}
	if err := s.SetConsent(m.url(""), true); err != nil || !s.Doc().Available {
		t.Fatal("consent makes the map available")
	}
	var saved struct{ Allowed map[string]int64 }
	b, _ := os.ReadFile(filepath.Join(dir, "consent.json"))
	if json.Unmarshal(b, &saved) != nil || saved.Allowed[m.url("")] == 0 {
		t.Error("consent persisted")
	}

	if _, err := s.Get("day", 3, 1, 2, true); status(err) != 404 || m.count() != 0 {
		t.Error("a prefetch of an uncached tile is refused, nothing fetched")
	}
	day, err := s.Get("day", 3, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	night, err := s.Get("night", 3, 1, 2, true)
	if err != nil || m.count() != 1 {
		t.Error("day and night share one upstream fetch; a prefetch of a cached tile is served")
	}
	h := m.requests[0]
	if h.Get("User-Agent") != UserAgent || h.Get("Cache-Control") != "" || h.Get("Pragma") != "" {
		t.Error("own User-Agent, no no-cache headers")
	}
	cfg, _ := png.DecodeConfig(bytes.NewReader(day))
	pal, isPal := cfg.ColorModel.(color.Palette)
	if !isPal || cfg.Width != 256 || cfg.Height != 256 || len(pal) > PanelColors {
		t.Error("panel tile is a 256x256 palette PNG within the colour budget")
	}
	dr, dg, db := rgbAt(t, day, 5, 5)
	nr, ng, nb := rgbAt(t, night, 5, 5)
	if nr+ng+nb >= (dr+dg+db)/3 {
		t.Error("night land is much darker than day")
	}
	rr, rg, rb := rgbAt(t, night, 5, 122)
	if rr+rg+rb <= (nr+ng+nb)*2 {
		t.Error("night roads stay lighter than land")
	}
	if dr&7 != dr>>5 || db&7 != db>>5 || dg&3 != dg>>6 {
		t.Errorf("colours snapped to RGB565: %d %d %d", dr, dg, db)
	}

	s.Get("day", 3, 1, 2, false)
	if m.count() != 1 {
		t.Error("fresh tile served from cache")
	}

	// past max-age, inside stale-while-revalidate: served at once, revalidated behind
	c.add(200)
	s.Get("day", 3, 1, 2, false)
	if !waitFor(func() bool { return m.count() == 2 }) {
		t.Error("stale tile triggers a background revalidation")
	}
	if m.requests[1].Get("If-None-Match") != `"v1"` {
		t.Error("revalidation is conditional (ETag)")
	}
	waitFor(func() bool { return s.Revalidating() == 0 })
	s.Get("day", 3, 1, 2, false)
	time.Sleep(200 * time.Millisecond)
	if m.count() != 2 {
		t.Error("304 refreshes the expiry")
	}

	// past the stale window: synchronous revalidation
	c.add(100 + MinKeep + 10)
	s.Get("day", 3, 1, 2, false)
	if m.count() != 3 {
		t.Error("expired tile revalidated before serving")
	}

	// concurrency cap and same-tile dedupe
	m.set("ok", 300*time.Millisecond)
	before := m.count()
	m.mu.Lock()
	m.maxActive = 0
	m.mu.Unlock()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		x, y := i, 5
		if i >= 6 {
			x, y = 0, 9
		}
		wg.Add(1)
		go func() { defer wg.Done(); s.Get("day", 6, x, y, false) }()
	}
	wg.Wait()
	m.set("ok", 0)
	if m.maxActive > UpstreamConcurrency {
		t.Errorf("at most %d upstream requests at once, saw %d", UpstreamConcurrency, m.maxActive)
	}
	if m.count()-before != 7 {
		t.Errorf("concurrent requests for one tile fetch it once: %d", m.count()-before)
	}

	// 429: back off, serve stale meanwhile, refuse uncached
	m.set("429", 0)
	c.add(MinKeep * 3)
	before = m.count()
	if got, err := s.Get("day", 3, 1, 2, false); err != nil || len(got) == 0 {
		t.Error("stale tile served when the server says 429")
	}
	if _, err := s.Get("day", 7, 1, 1, false); status(err) != 503 || m.count() != before+1 {
		t.Error("backoff honours Retry-After without more requests")
	}
	c.add(31)
	m.set("ok", 0)
	s.Get("day", 7, 1, 1, false)
	if m.count() != before+2 {
		t.Error("requests resume after Retry-After")
	}

	if _, err := s.Get("day", 20, 0, 0, false); status(err) != 400 {
		t.Error("bad coordinates rejected")
	}
	if _, err := s.Get("dusk", 1, 0, 0, false); status(err) != 400 {
		t.Error("bad theme rejected")
	}

	// deny mode and URL changes
	s.SetSettings(map[string]any{"url": m.url(""), "tiles": "deny"})
	if d := s.Doc(); d.Available || d.Reason != "tiles disabled in kismet" {
		t.Error("deny mode turns tiles off")
	}
	s.SetSettings(map[string]any{"url": m.url("/other"), "tiles": "ask"})
	if s.Doc().Consent != "pending" {
		t.Error("a new tile URL asks again")
	}
	s.SetConsent(m.url("/other"), false)
	if s.Doc().Consent != "declined" {
		t.Error("'not now' is remembered for the session")
	}
	if service(t, dir, c, m.url("/other")).Doc().Consent != "pending" {
		t.Error("'not now' is not persisted")
	}
	s3 := service(t, dir, c, m.url(""))
	if s3.Doc().Consent != "allowed" {
		t.Error("allowed consent survives a restart")
	}

	s3.MaxBytes = 20_000
	s3.Evict()
	var total int64
	filepath.WalkDir(filepath.Join(dir, "tiles"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			total += info.Size()
		}
		return nil
	})
	if total > 20_000 {
		t.Errorf("eviction trims the cache under its cap: %d", total)
	}
}

func TestVariantQuantizes(t *testing.T) {
	// a gradient with far more colours than the panel budget
	img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), uint8(x + y), uint8(255 - y/4)})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	for sk, th := range Skins {
		out, err := MakeVariant(b.Bytes(), sk)
		if err != nil {
			t.Fatal(err)
		}
		cfg, _ := png.DecodeConfig(bytes.NewReader(out))
		if pal, ok := cfg.ColorModel.(color.Palette); !ok || len(pal) > PanelColors || cfg.Width != 256 {
			t.Errorf("%s: odd-sized RGBA tile scaled and cut to the colour budget", th)
		}
	}
}

// Half and quarter turns land on whole pixels, so the turned tile is the
// tile under it turned exactly
func TestTurned(t *testing.T) {
	m := newMock()
	defer m.srv.Close()
	s := service(t, t.TempDir(), &clock{t: 1000}, m.url(""))
	s.SetConsent(m.url(""), true)
	const z = 3
	off := 2 << z
	plain, err := s.Get("classic", z, 2, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	want := fromImage(mustDecode(t, plain))
	// 180°: turned square (x, y) is world square (-x-1, -y-1)
	// 90°: turned (x, y) is world (-y-1, x)
	for _, c := range []struct {
		rot, x, y int
		src       func(i, j int) (int, int)
	}{
		{18, -2 - 1 + off, -5 - 1 + off, func(i, j int) (int, int) { return 255 - i, 255 - j }},
		{9, 5 + off, -2 - 1 + off, func(i, j int) (int, int) { return 255 - j, i }},
	} {
		b, err := s.GetTurned("classic", z, c.x, c.y, c.rot, false)
		if err != nil {
			t.Fatalf("rot %d: %v", c.rot, err)
		}
		got := fromImage(mustDecode(t, b))
		bad := 0
		for j := 0; j < 256; j++ {
			for i := 0; i < 256; i++ {
				si, sj := c.src(i, j)
				if got.pal[got.idx[j*256+i]] != want.pal[want.idx[sj*256+si]] {
					bad++
				}
			}
		}
		if bad > 0 {
			t.Errorf("rot %d: %d pixels differ from the tile turned", c.rot, bad)
		}
		again, err := s.GetTurned("classic", z, c.x, c.y, c.rot, true)
		if err != nil || !bytes.Equal(again, b) {
			t.Errorf("rot %d from the cache: %v", c.rot, err)
		}
	}
	if _, err := s.GetTurned("classic", z, 6+off, off, 9, true); status(err) != 404 {
		t.Errorf("prefetch of uncached tiles: %v", err)
	}
	if _, err := s.GetTurned("classic", z, 0, 0, RotSteps, false); status(err) != 400 {
		t.Errorf("bad rot: %v", err)
	}
}

func mustDecode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}
