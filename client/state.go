package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
	"unicode"

	"kismet-crowpanel/markers"
	"kismet-crowpanel/tiles"
)

// Limits match ui/widget_model.h
const (
	histLen     = 60
	maxPhys     = 8
	maxSources  = 8
	maxDevices  = 16
	maxAlerts   = 8
	maxChannels = 48

	activeWindow = 60
	newWindow    = 300
)

type gpsDoc struct {
	Fix     int     `json:"fix"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Alt     float64 `json:"alt"`
	Speed   float64 `json:"speed"`
	Heading float64 `json:"heading"`
	Dist    int     `json:"dist"`
}

type phyDoc struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type devicesDoc struct {
	Total  int      `json:"total"`
	Active int      `json:"active"`
	New    int      `json:"new"`
	Phys   []phyDoc `json:"phys"`
}

type sourceDoc struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	Channel string  `json:"channel"`
	Packets int64   `json:"packets"`
	PPS     int64   `json:"pps"`
	Running bool    `json:"running"`
	Error   bool    `json:"error"`
	Hopping bool    `json:"hopping"`
	Hist    []int64 `json:"hist"` // packets/s, oldest first
}

type topDoc struct {
	Name    string `json:"name"`
	MAC     string `json:"mac"`
	Phy     string `json:"phy"`
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Signal  int    `json:"signal"`
	Age     int    `json:"age"`
}

type alertDoc struct {
	Clock    string `json:"clock"`
	Header   string `json:"header"`
	Text     string `json:"text"`
	Severity int    `json:"severity"`
}

type alertsDoc struct {
	Total  int        `json:"total"`
	Recent []alertDoc `json:"recent"`
}

// stateDoc is what the panel parses (ui/widget_model.cpp)
type stateDoc struct {
	V        int         `json:"v"`
	Link     string      `json:"link"`
	Message  string      `json:"message"`
	Server   string      `json:"server"`
	Clock    string      `json:"clock"`
	Uptime   int64       `json:"uptime"`
	Sun      string      `json:"sun"`
	GPS      gpsDoc      `json:"gps"`
	Devices  devicesDoc  `json:"devices"`
	Channels [][2]int    `json:"channels"`
	PPS      int64       `json:"pps"`
	PPSHist  []int64     `json:"pps_hist"`
	Sources  []sourceDoc `json:"sources"`
	Top      []topDoc    `json:"top"`
	Alerts   alertsDoc   `json:"alerts"`
	Map      *tiles.Doc  `json:"map,omitempty"`
}

func emptyDoc(link, message, server string) stateDoc {
	return stateDoc{V: 1, Link: link, Message: message, Server: server, Clock: clockStr(time.Now()),
		Devices: devicesDoc{Phys: []phyDoc{}}, Channels: [][2]int{}, PPSHist: []int64{},
		Sources: []sourceDoc{}, Top: []topDoc{}, Alerts: alertsDoc{Recent: []alertDoc{}}}
}

func clockStr(t time.Time) string { return t.Format("15:04:05") }

func unixTime(ts float64) time.Time { return time.Unix(0, int64(ts*1e9)) }

func nowSecs() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func shortPhy(name string) string {
	if len(name) >= 4 && name[:4] == "IEEE" {
		return name[4:]
	}
	return name
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) < n {
		return s
	}
	return string(r[:n-2]) + "~"
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func rad(d float64) float64 { return d * math.Pi / 180 }
func deg(r float64) float64 { return r * 180 / math.Pi }

func pmod(a, b float64) float64 { return a - b*math.Floor(a/b) }

// sunElevation is the approximate solar elevation in degrees (good to ~1
// degree).
func sunElevation(lat, lon, t float64) float64 {
	d := t/86400 - 10957.5 // days since J2000
	g := rad(pmod(357.529+0.98560028*d, 360))
	q := pmod(280.459+0.98564736*d, 360)
	lam := rad(q + 1.915*math.Sin(g) + 0.020*math.Sin(2*g))
	eps := rad(23.439 - 0.00000036*d)
	ra := math.Atan2(math.Cos(eps)*math.Sin(lam), math.Cos(lam))
	dec := math.Asin(math.Sin(eps) * math.Sin(lam))
	gmst := pmod(18.697374558+24.06570982441908*d, 24)
	ha := rad(gmst*15+lon) - ra
	la := rad(lat)
	return deg(math.Asin(math.Sin(la)*math.Sin(dec) + math.Cos(la)*math.Cos(dec)*math.Cos(ha)))
}

func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := rad(lat1), rad(lat2)
	dp, dl := p2-p1, rad(lon2-lon1)
	a := math.Pow(math.Sin(dp/2), 2) + math.Cos(p1)*math.Cos(p2)*math.Pow(math.Sin(dl/2), 2)
	return 6371000 * 2 * math.Asin(math.Sqrt(a))
}

// channelNumber is the leading channel number of a Kismet channel string
// ("36HT40+" -> 36).
func channelNumber(ch string) int {
	i := 0
	for i < len(ch) && unicode.IsDigit(rune(ch[i])) {
		i++
	}
	n, _ := strconv.Atoi(ch[:i])
	return n
}

type seenDevice struct {
	phy, channel string
	last         float64
}

func channelCounts(devs []seenDevice, now float64) [][2]int {
	counts := map[int]int{}
	for _, d := range devs {
		if now-d.last > activeWindow || !hasSuffix(d.phy, "802.11") {
			continue
		}
		if ch := channelNumber(d.channel); ch > 0 && ch < 200 {
			counts[ch]++
		}
	}
	out := make([][2]int, 0, len(counts))
	for c, n := range counts {
		out = append(out, [2]int{c, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out[:min(len(out), maxChannels)]
}

func hasSuffix(s, suf string) bool { return len(s) >= len(suf) && s[len(s)-len(suf):] == suf }

// gpsTrack keeps session distance and sun state from successive fixes.
type gpsTrack struct {
	have     bool
	lat, lon float64
	dist     float64
	sun      string
}

func (g *gpsTrack) update(fix int, lat, lon, now float64) {
	if fix < 2 {
		return
	}
	if g.have {
		// ignore jitter while parked and jumps from bad fixes
		if step := haversineM(g.lat, g.lon, lat, lon); step > 3 && step < 1000 {
			g.dist += step
			g.lat, g.lon = lat, lon
		}
	} else {
		g.have, g.lat, g.lon = true, lat, lon
	}

	// hysteresis so dusk doesn't flap the theme
	switch el := sunElevation(lat, lon, now); {
	case el > -2:
		g.sun = "up"
	case el < -4:
		g.sun = "down"
	case g.sun == "":
		g.sun = "down"
	}
}

func (g *gpsTrack) doc(fix int, lat, lon, alt, speed, heading float64) gpsDoc {
	return gpsDoc{fix, round(lat, 6), round(lon, 6), round(alt, 1), round(speed, 1), round(heading, 1), int(g.dist)}
}

// ring keeps the last n values
type ring[T any] struct {
	n    int
	vals []T
}

func (r *ring[T]) push(v T) {
	r.vals = append(r.vals, v)
	if len(r.vals) > r.n {
		r.vals = r.vals[1:]
	}
}

// pushFront for newest-first lists
func (r *ring[T]) pushFront(v T) {
	r.vals = append([]T{v}, r.vals...)
	if len(r.vals) > r.n {
		r.vals = r.vals[:r.n]
	}
}

func (r *ring[T]) list() []T { return append([]T{}, r.vals...) }

type poller interface {
	poll() error
}

// state holds the latest state document; a poller fills it.
type state struct {
	name    string
	mu      sync.Mutex
	doc     stateDoc
	ppsHist ring[int64]
	tiles   *tiles.Service
	devmap  *markers.DeviceMap
	trail   *markers.TrackLog
}

func newState(name string) *state {
	return &state{name: name, doc: emptyDoc("waiting", "waiting for kismet", name),
		ppsHist: ring[int64]{n: histLen}, devmap: markers.NewDeviceMap(), trail: &markers.TrackLog{}}
}

func (s *state) mapDoc() tiles.Doc {
	if s.tiles == nil {
		return tiles.Off
	}
	return s.tiles.Doc()
}

func (s *state) snapshot() []byte {
	m := s.mapDoc()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc.Clock = clockStr(time.Now())
	s.doc.Map = &m
	b, _ := json.Marshal(s.doc)
	return b
}

func (s *state) publish(d stateDoc) {
	s.mu.Lock()
	s.doc = d
	s.mu.Unlock()
}

func (s *state) markDown(msg string) {
	s.mu.Lock()
	s.doc.Link = "kismet_down"
	s.doc.Message = clip(msg, 60)
	s.mu.Unlock()
}

func (s *state) run(p poller, interval time.Duration) {
	for {
		t0 := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("ERROR poll failed: %v", r)
					s.markDown("client error, see log")
				}
			}()
			if err := p.poll(); err != nil {
				log.Printf("WARNING %v", err)
				s.markDown(err.Error())
			}
		}()
		time.Sleep(max(200*time.Millisecond, interval-time.Since(t0)))
	}
}

func debugf(format string, a ...any) {
	if verbose {
		log.Printf("DEBUG "+format, a...)
	}
}

var verbose bool

// JSON helpers for Kismet's loosely typed documents

func num(m map[string]any, k string) float64 {
	switch v := m[k].(type) {
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

func str(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return v
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func objs(v any) []map[string]any {
	a, _ := v.([]any)
	out := make([]map[string]any, 0, len(a))
	for _, e := range a {
		out = append(out, obj(e))
	}
	return out
}
