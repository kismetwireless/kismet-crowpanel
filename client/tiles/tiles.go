// Package tiles serves map tiles to the panel.
//
// Follows Kismet's map tile setting (/ui/map_tiles: url, attribution,
// ask|deny) and asks for consent per tile server, as Kismet's web UI does.
// Tiles are fetched only when the panel asks for one it's showing, then
// cached; there is no region prefetch (OSMF tile policy).  Each tile is
// converted to a small palette PNG for the panel, in a day and a night
// variant.
package tiles

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	OSMURL         = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
	OSMAttribution = "© OpenStreetMap contributors"
	UserAgent      = "kismet-crowpanel/0.4"

	MaxZoom             = 19
	MinKeep             = 7 * 86400 // OSMF: keep tiles at least 7 days
	UpstreamConcurrency = 2
	backoffMin          = 5.0
	backoffMax          = 300.0
	FetchTimeout        = 10 * time.Second
	lockStripes         = 64
)

type Error struct {
	Status int
	Reason string
}

func (e *Error) Error() string { return e.Reason }

type Settings struct {
	URL, Attribution, Tiles string
}

// FromKismet normalises a /ui/map_tiles document the same way Kismet's web
// UI does: no URL means OpenStreetMap.
func FromKismet(doc map[string]any) Settings {
	str := func(k string) string { s, _ := doc[k].(string); return s }
	s := Settings{URL: str("url"), Attribution: str("attribution"), Tiles: str("tiles")}
	if s.Tiles != "ask" && s.Tiles != "deny" {
		s.Tiles = "ask"
	}
	if s.URL == "" {
		s.URL = OSMURL
		if s.Attribution == "" {
			s.Attribution = OSMAttribution
		}
	}
	return s
}

var (
	maxAgeRe = regexp.MustCompile(`max-age=(\d+)`)
	swrRe    = regexp.MustCompile(`stale-while-revalidate=(\d+)`)
)

// expiry gives (fresh until, stale until).  Fresh per Cache-Control max-age
// or Expires; after that the cached tile is served while it's revalidated
// in the background, for stale-while-revalidate and at least MinKeep.
func expiry(h http.Header, now float64) (float64, float64) {
	cc := h.Get("Cache-Control")
	fresh := -1.0
	if m := maxAgeRe.FindStringSubmatch(cc); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		fresh = now + v
	} else if e := h.Get("Expires"); e != "" {
		if t, err := http.ParseTime(e); err == nil {
			fresh = float64(t.Unix())
		}
	}
	if fresh < 0 {
		fresh = now + MinKeep
	}
	stale := fresh
	if m := swrRe.FindStringSubmatch(cc); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		stale = fresh + v
	}
	return fresh, max(stale, now+MinKeep)
}

// consentStore: allowed tile servers persist (consent.json); "not now"
// lasts until the client restarts.  Keyed by URL template, so a new URL
// asks again.
type consentStore struct {
	path     string
	mu       sync.Mutex
	allowed  map[string]int64
	declined map[string]bool
}

func loadConsent(path string) *consentStore {
	c := &consentStore{path: path, allowed: map[string]int64{}, declined: map[string]bool{}}
	if b, err := os.ReadFile(path); err == nil {
		var doc struct {
			Allowed map[string]int64 `json:"allowed"`
		}
		if json.Unmarshal(b, &doc) == nil && doc.Allowed != nil {
			c.allowed = doc.Allowed
		}
	}
	return c
}

func (c *consentStore) status(url string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.allowed[url]; ok {
		return "allowed"
	}
	if c.declined[url] {
		return "declined"
	}
	return "pending"
}

func (c *consentStore) set(url string, allow bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if allow {
		c.allowed[url] = time.Now().Unix()
		delete(c.declined, url)
	} else {
		delete(c.allowed, url)
		c.declined[url] = true
	}
	b, _ := json.MarshalIndent(map[string]any{"allowed": c.allowed}, "", " ")
	return writeFile(c.path, b)
}

type meta struct {
	ETag         string  `json:"etag"`
	LastModified string  `json:"last_modified"`
	FreshUntil   float64 `json:"fresh_until"`
	StaleUntil   float64 `json:"stale_until"`
	Fetched      float64 `json:"fetched,omitempty"`
}

type Service struct {
	dir      string
	MaxBytes int64
	Now      func() float64
	client   *http.Client
	consent  *consentStore

	mu           sync.Mutex
	settings     Settings
	haveSettings bool
	backoffUntil float64
	backoffStep  float64
	writes       int
	revalidating map[string]bool

	upstream chan struct{}
	stripes  [lockStripes]sync.Mutex
}

func New(cacheDir string, maxBytes int64, timeout time.Duration) (*Service, error) {
	if err := os.MkdirAll(filepath.Join(cacheDir, "tiles"), 0o755); err != nil {
		return nil, err
	}
	return &Service{
		dir:          cacheDir,
		MaxBytes:     maxBytes,
		Now:          func() float64 { return float64(time.Now().UnixNano()) / 1e9 },
		client:       &http.Client{Timeout: timeout},
		consent:      loadConsent(filepath.Join(cacheDir, "consent.json")),
		settings:     Settings{Tiles: "deny"},
		revalidating: map[string]bool{},
		upstream:     make(chan struct{}, UpstreamConcurrency),
	}, nil
}

func (s *Service) SetSettings(doc map[string]any) {
	n := FromKismet(doc)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n != s.settings {
		log.Printf("INFO map tiles: %s (%s)", n.URL, n.Tiles)
	}
	s.settings = n
	s.haveSettings = true
}

func (s *Service) current() (Settings, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, s.haveSettings
}

func hostOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

func (s *Service) SetConsent(url string, allow bool) error {
	cur, _ := s.current()
	if url != cur.URL {
		return &Error{409, "tile server changed; ask again"}
	}
	if err := s.consent.set(url, allow); err != nil {
		return &Error{500, err.Error()}
	}
	word := "declined"
	if allow {
		word = "allowed"
	}
	log.Printf("INFO map tiles from %s %s", hostOf(url), word)
	return nil
}

type Doc struct {
	Tiles       string `json:"tiles"`
	Host        string `json:"host"`
	Attribution string `json:"attribution"`
	Consent     string `json:"consent"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason"`
	URL         string `json:"url"`
}

var Off = Doc{Tiles: "deny", Consent: "pending", Reason: "map disabled"}

func (s *Service) Doc() Doc {
	cur, have := s.current()
	d := Doc{Tiles: cur.Tiles, Attribution: cur.Attribution, Consent: "pending"}
	if have {
		d.Consent = s.consent.status(cur.URL)
		d.Host = hostOf(cur.URL)
		d.URL = cur.URL
	}
	switch {
	case !have:
		d.Reason = "waiting for kismet"
	case cur.Tiles == "deny":
		d.Reason = "tiles disabled in kismet"
	case d.Consent != "allowed":
		d.Reason = "needs consent"
	}
	d.Available = d.Reason == ""
	return d
}

func (s *Service) sourceDir(u string) string {
	h := sha1.Sum([]byte(u))
	return filepath.Join(s.dir, "tiles", hex.EncodeToString(h[:])[:12])
}

func (s *Service) stripe(key string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(key))
	return &s.stripes[h.Sum32()%lockStripes]
}

// Get returns a panel tile; cachedOnly (prefetches) never goes to the tile
// server, and fails with 404 for a tile that isn't cached.
func (s *Service) Get(skin string, z, x, y int, cachedOnly bool) ([]byte, error) {
	sk := SkinID(skin)
	if sk < 0 {
		return nil, &Error{400, "bad skin"}
	}
	if z < 0 || z > MaxZoom || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		return nil, &Error{400, "bad tile coordinates"}
	}
	if d := s.Doc(); !d.Available {
		return nil, &Error{403, d.Reason}
	}

	cur, _ := s.current()
	base := s.sourceDir(cur.URL)
	zxy := filepath.Join(strconv.Itoa(z), strconv.Itoa(x), strconv.Itoa(y)+".png")
	origPath := filepath.Join(base, zxy)
	// v2: by skin; v1 was day and night
	varPath := filepath.Join(base, "panel", "v2", Skins[sk], zxy)

	key := fmt.Sprint(cur.URL, z, x, y)
	mu := s.stripe(key)
	mu.Lock()
	defer mu.Unlock()
	var png []byte
	if cachedOnly {
		if png, _ = load(origPath); png == nil {
			return nil, &Error{404, "not cached"}
		}
	} else {
		var err error
		if png, err = s.original(cur.URL, key, origPath, z, x, y); err != nil {
			return nil, err
		}
	}
	if vs, err := os.Stat(varPath); err == nil {
		if os_, err := os.Stat(origPath); err == nil && !vs.ModTime().Before(os_.ModTime()) {
			if b, err := os.ReadFile(varPath); err == nil {
				now := time.Now()
				os.Chtimes(varPath, now, now)
				return b, nil
			}
		}
	}
	out, err := MakeVariant(png, sk)
	if err != nil {
		return nil, &Error{502, "bad tile image: " + err.Error()}
	}
	s.write(varPath, out)
	return out, nil
}

func (s *Service) original(u, key, path string, z, x, y int) ([]byte, error) {
	data, m := load(path)
	now := s.Now()
	if data != nil && now < m.FreshUntil {
		touch(path, now)
		return data, nil
	}
	if data != nil && now < m.StaleUntil {
		touch(path, now)
		s.revalidateLater(u, key, path, z, x, y)
		return data, nil
	}
	return s.refresh(u, path, z, x, y, data, m)
}

func load(path string) ([]byte, meta) {
	var m meta
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, m
	}
	mb, err := os.ReadFile(path + ".json")
	if err != nil || json.Unmarshal(mb, &m) != nil {
		return nil, meta{}
	}
	return data, m
}

// touch marks a tile used for eviction: atime is use order
func touch(path string, now float64) {
	if st, err := os.Stat(path); err == nil {
		os.Chtimes(path, time.Unix(0, int64(now*1e9)), st.ModTime())
	}
}

func (s *Service) Revalidating() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.revalidating)
}

func (s *Service) revalidateLater(u, key, path string, z, x, y int) {
	s.mu.Lock()
	if s.revalidating[key] || s.Now() < s.backoffUntil {
		s.mu.Unlock()
		return
	}
	s.revalidating[key] = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.revalidating, key)
			s.mu.Unlock()
		}()
		mu := s.stripe(key)
		mu.Lock()
		defer mu.Unlock()
		if data, m := load(path); data != nil && s.Now() >= m.FreshUntil {
			s.refresh(u, path, z, x, y, data, m)
		}
	}()
}

// refresh fetches or revalidates; a stale copy is returned on any failure.
func (s *Service) refresh(u, path string, z, x, y int, data []byte, m meta) ([]byte, error) {
	s.mu.Lock()
	backingOff := s.Now() < s.backoffUntil
	s.mu.Unlock()
	if backingOff {
		if data != nil {
			return data, nil
		}
		return nil, &Error{503, "tile server asked us to back off"}
	}

	r := strings.NewReplacer("{z}", strconv.Itoa(z), "{x}", strconv.Itoa(x), "{y}", strconv.Itoa(y))
	req, err := http.NewRequest("GET", r.Replace(u), nil)
	if err != nil {
		return nil, &Error{502, "bad tile URL"}
	}
	req.Header.Set("User-Agent", UserAgent)
	if data != nil {
		if m.ETag != "" {
			req.Header.Set("If-None-Match", m.ETag)
		}
		if m.LastModified != "" {
			req.Header.Set("If-Modified-Since", m.LastModified)
		}
	}

	s.upstream <- struct{}{}
	status, body, rh := s.fetch(req)
	<-s.upstream

	now := s.Now()
	if status == 304 && data != nil {
		m.FreshUntil, m.StaleUntil = expiry(rh, now)
		b, _ := json.Marshal(m)
		s.write(path+".json", b)
		s.resetBackoff()
		return data, nil
	}
	if status == 200 && bytes.HasPrefix(body, []byte("\x89PNG")) {
		s.resetBackoff()
		fresh, stale := expiry(rh, now)
		s.write(path, body)
		b, _ := json.Marshal(meta{rh.Get("ETag"), rh.Get("Last-Modified"), fresh, stale, now})
		s.write(path+".json", b)
		return body, nil
	}
	if status == 429 || status >= 500 || status == 0 {
		s.backoff(rh)
	}
	if status == 404 {
		return nil, &Error{404, "no such tile"}
	}
	if data != nil {
		return data, nil
	}
	if status == 0 {
		return nil, &Error{504, "tile server: unreachable"}
	}
	return nil, &Error{502, fmt.Sprint("tile server: ", status)}
}

func (s *Service) fetch(req *http.Request) (int, []byte, http.Header) {
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("WARNING tile fetch failed: %v", err)
		return 0, nil, http.Header{}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		log.Printf("WARNING tile fetch failed: %v", err)
		return 0, nil, resp.Header
	}
	return resp.StatusCode, body, resp.Header
}

func (s *Service) resetBackoff() {
	s.mu.Lock()
	s.backoffStep = 0
	s.mu.Unlock()
}

func (s *Service) backoff(h http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backoffStep = min(backoffMax, max(backoffMin, s.backoffStep*2))
	wait := s.backoffStep
	if v, err := strconv.ParseFloat(h.Get("Retry-After"), 64); err == nil {
		wait = v
	}
	s.backoffUntil = s.Now() + wait
	log.Printf("WARNING tile server backoff for %.0fs", wait)
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func (s *Service) write(path string, data []byte) {
	if err := writeFile(path, data); err != nil {
		log.Printf("WARNING tile cache: %v", err)
		return
	}
	s.mu.Lock()
	s.writes++
	sweep := s.writes%200 == 0
	s.mu.Unlock()
	if sweep {
		s.Evict()
	}
}

// Evict drops least recently used files until the cache is under its cap.
func (s *Service) Evict() {
	type file struct {
		used time.Time
		size int64
		path string
	}
	var files []file
	var total int64
	filepath.WalkDir(filepath.Join(s.dir, "tiles"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files = append(files, file{accessTime(info), info.Size(), p})
			total += info.Size()
		}
		return nil
	})
	if total <= s.MaxBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	for _, f := range files {
		if float64(total) <= float64(s.MaxBytes)*0.9 {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	log.Printf("INFO tile cache trimmed to %.0f MB", float64(total)/1e6)
}
