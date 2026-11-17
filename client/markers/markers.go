// Package markers holds the devices on the map.
//
// Every device with a location goes into a grid index (zoom-16 tiles).  The
// panel asks for the markers around its view; they come back clustered on a
// 24 px grid at the view's zoom, in a compact binary form (ui/widget_map.h):
//
//	header:  seq u16, z u8, marker size u8, count u16
//	marker:  x u32, y u32 (world px at z), id u32 (count for a cluster),
//	         phy u8, flags u8 (1 new, 2 cluster, 4 reported), peak dBm i8,
//	         age bucket u8, heading u8 (360/256 deg), icon u8
//
// A reported marker is at the position the device gives for itself (an
// aircraft's ADS-B position, a vessel's AIS one) and is never clustered.
//
// The track is our own path, sent as E7 lat/lon pairs:
//
//	version u32, count u16, then count x (lat i32, lon i32)
package markers

import (
	"encoding/binary"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	HeadSize   = 6
	MarkerSize = 18

	IndexZoom    = 16
	ClusterPx    = 24
	MaxMarkers   = 300
	NewSecs      = 60
	RecentSecs   = 600
	PhyAll       = 0x1f
	FlagNew      = 1
	FlagCluster  = 2
	FlagReported = 4

	TrackMinStepM = 15
	TrackKeep     = 4000
	TrackSend     = 400
)

// matches the phy colours on the panel
const (
	PhyWifi = iota
	PhyBTLE
	PhyBT
	PhySDR
	PhyOther
)

// Icons the panel draws, as ui/widget_icons.h (scripts/gen-icons.py)
const (
	IconNone = iota
	IconWifi
	IconBluetooth
	IconPlane
	IconHelicopter
	IconVessel
	IconTower
	IconAnchor
	IconLifebuoy
	IconDrone
	IconZigbee
	IconGauge
	IconMeter
	IconMesh
	IconRadio
)

// IconOf picks a device's icon the way Kismet's web UI does: by phy, then
// the ADS-B aircraft type, the AIS station class, or a UAV match.
func IconOf(phy, devType, aisClass string, uavMatch int) uint8 {
	if uavMatch != 0 {
		return IconDrone
	}
	switch phy {
	case "IEEE802.11":
		return IconWifi
	case "Bluetooth", "BTLE":
		return IconBluetooth
	case "ADSB":
		if devType == "Helicopter" || devType == "Gyroplane" {
			return IconHelicopter
		}
		return IconPlane
	case "AIS":
		switch aisClass {
		case "Base Station":
			return IconTower
		case "AtoN":
			return IconAnchor
		case "SAR Aircraft":
			return IconPlane
		case "AIS-SART", "AIS-MOB", "AIS-EPIRB":
			return IconLifebuoy
		}
		return IconVessel
	case "802.15.4":
		return IconZigbee
	case "RFSENSOR":
		return IconGauge
	case "METER":
		return IconMeter
	case "Meshtastic", "MeshCore":
		return IconMesh
	case "Cellular":
		return IconTower
	}
	return IconRadio
}

// ReportsPosition is true for phys whose devices give their own position;
// until they have, the only location is where we heard them.
func ReportsPosition(phy string) bool {
	return phy == "ADSB" || phy == "AIS"
}

var ageBuckets = [...]float64{60, 600, 3600} // now, under 10 min, under 1 h, older

func PhyIndex(name string) int {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "802.11"):
		return PhyWifi
	case strings.Contains(n, "btle"):
		return PhyBTLE
	case strings.Contains(n, "bluetooth"):
		return PhyBT
	case strings.Contains(n, "rtl"), strings.Contains(n, "sdr"), strings.Contains(n, "radiation"),
		n == "adsb", n == "ais", n == "meter", n == "rfsensor":
		return PhySDR
	}
	return PhyOther
}

func WorldPx(lat, lon float64, z int) (float64, float64) {
	scale := 256 * float64(uint64(1)<<z)
	lat = max(-85.05112878, min(85.05112878, lat))
	s := math.Sin(lat * math.Pi / 180)
	return (lon + 180) / 360 * scale, (0.5 - math.Log((1+s)/(1-s))/(4*math.Pi)) * scale
}

func ageBucket(age float64) uint8 {
	for i, limit := range ageBuckets {
		if age < limit {
			return uint8(i)
		}
	}
	return uint8(len(ageBuckets))
}

func clockStr(ts float64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(int64(ts), 0).Format("15:04:05")
}

// View is a request for the markers around a w x h view centred on lat/lon.
type View struct {
	Seq, Z   int
	Lat, Lon float64
	W, H     int
	PhyMask  int
	NewOnly  bool
	MaxAge   int // seen in the last this many seconds, 0 for any
}

// ViewFrom reads a view from the panel's view frame or a query string:
// get(name) gives a field and whether it's there.
func ViewFrom(get func(string) (string, bool)) (View, error) {
	v := View{PhyMask: PhyAll}
	num := func(name string, required bool, f func(string) error) error {
		s, ok := get(name)
		if !ok {
			if required {
				return &missingError{name}
			}
			return nil
		}
		return f(s)
	}
	atoi := func(p *int) func(string) error {
		return func(s string) (err error) { *p, err = strconv.Atoi(s); return }
	}
	atof := func(p *float64) func(string) error {
		return func(s string) (err error) { *p, err = strconv.ParseFloat(s, 64); return }
	}
	var nw, recent int
	for _, f := range []error{
		num("seq", false, atoi(&v.Seq)), num("z", true, atoi(&v.Z)), num("lat", true, atof(&v.Lat)),
		num("lon", true, atof(&v.Lon)), num("w", true, atoi(&v.W)), num("h", true, atoi(&v.H)),
		num("phy", false, atoi(&v.PhyMask)), num("new", false, atoi(&nw)), num("age", false, atoi(&v.MaxAge)),
		num("recent", false, atoi(&recent)),
	} {
		if f != nil {
			return v, f
		}
	}
	v.PhyMask &= PhyAll
	v.NewOnly = nw != 0
	// older panels sent recent=1 for the last 10 minutes
	if v.MaxAge <= 0 && recent != 0 {
		v.MaxAge = RecentSecs
	}
	v.MaxAge = max(0, v.MaxAge)
	return v, nil
}

type missingError struct{ name string }

func (e *missingError) Error() string { return "missing " + e.name }

// Geopoint takes Kismet's [lon, lat]; ok is false for a missing or zero
// location.
func Geopoint(v any) (lat, lon float64, ok bool) {
	a, isList := v.([]any)
	if !isList || len(a) < 2 {
		return 0, 0, false
	}
	lo, ok1 := a[0].(float64)
	la, ok2 := a[1].(float64)
	if !ok1 || !ok2 || (la == 0 && lo == 0) || la < -90 || la > 90 || lo < -180 || lo > 180 {
		return 0, 0, false
	}
	return la, lo, true
}

// Info is what's known about a device besides where it is.  Times are on
// the feeder's clock.
type Info struct {
	Name, MAC, Phy, Type, Channel string
	Signal, Peak                  int
	First, Last                   float64
	Packets                       int64
	// Reported: lat/lon are the device's own position, with its heading
	// (degrees), altitude (m) and speed (km/h)
	Reported            bool
	Heading, Alt, Speed float64
	Icon                uint8 // IconNone to pick by phy and type
}

type device struct {
	id       uint32
	lat, lon float64
	Info
}

type cell struct{ x, y int64 }

// DeviceMap holds located devices, keyed by Kismet's device key.  Times
// are on the clock of whoever feeds the map (Kismet's, for a real server);
// the skew maps local time onto it between updates.
type DeviceMap struct {
	mu     sync.Mutex
	ids    map[string]uint32
	devs   map[uint32]*device
	grid   map[cell]map[uint32]struct{}
	nextID uint32
	skew   float64
	Now    func() float64 // local time, seconds
}

func NewDeviceMap() *DeviceMap {
	return &DeviceMap{ids: map[string]uint32{}, devs: map[uint32]*device{},
		grid: map[cell]map[uint32]struct{}{}, nextID: 1, Now: unixNow}
}

func unixNow() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (m *DeviceMap) now() float64 { return m.Now() + m.skew }

func (m *DeviceMap) SetClock(sourceNow float64) {
	m.mu.Lock()
	m.skew = sourceNow - m.Now()
	m.mu.Unlock()
}

func cellOf(lat, lon float64) cell {
	x, y := WorldPx(lat, lon, IndexZoom)
	return cell{int64(math.Floor(x / 256)), int64(math.Floor(y / 256))}
}

func (m *DeviceMap) addToGrid(c cell, id uint32) {
	s := m.grid[c]
	if s == nil {
		s = map[uint32]struct{}{}
		m.grid[c] = s
	}
	s[id] = struct{}{}
}

// Update adds or refreshes a device and returns its id.
func (m *DeviceMap) Update(key string, lat, lon float64, info Info) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.ids[key]
	if !ok {
		id = m.nextID
		m.nextID++
		m.ids[key] = id
		m.devs[id] = &device{id: id, lat: lat, lon: lon}
		m.addToGrid(cellOf(lat, lon), id)
	}
	d := m.devs[id]
	if d.lat != lat || d.lon != lon {
		if o, n := cellOf(d.lat, d.lon), cellOf(lat, lon); o != n {
			delete(m.grid[o], id)
			m.addToGrid(n, id)
		}
		d.lat, d.lon = lat, lon
	}
	d.Info = info
	return id
}

func (m *DeviceMap) ID(key string) (uint32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.ids[key]
	return id, ok
}

func (m *DeviceMap) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.devs)
}

// candidates are the devices in the world-pixel box at zoom z (a superset).
func (m *DeviceMap) candidates(x0, y0, x1, y1 float64, z int) []*device {
	f := math.Pow(2, float64(IndexZoom-z))
	tx0, ty0 := int64(math.Floor(x0*f/256)), int64(math.Floor(y0*f/256))
	tx1, ty1 := int64(math.Floor(x1*f/256)), int64(math.Floor(y1*f/256))
	var out []*device
	if (tx1-tx0+1)*(ty1-ty0+1) > int64(max(64, len(m.grid))) {
		for _, d := range m.devs {
			out = append(out, d)
		}
		return out
	}
	for tx := tx0; tx <= tx1; tx++ {
		for ty := ty0; ty <= ty1; ty++ {
			for id := range m.grid[cell{tx, ty}] {
				out = append(out, m.devs[id])
			}
		}
	}
	return out
}

type member struct {
	x, y float64
	d    device
}

// Markers for a view, with half a view of margin each side so the panel
// can pan without asking again.  Filters: PhyMask has a bit per phy;
// NewOnly keeps devices first seen in the last NewSecs, MaxAge those
// seen in the last MaxAge seconds.
func (m *DeviceMap) Markers(v View) []byte {
	z := max(0, min(22, v.Z))
	w := float64(max(1, min(4096, v.W)))
	h := float64(max(1, min(4096, v.H)))
	cx, cy := WorldPx(v.Lat, v.Lon, z)
	x0, y0, x1, y1 := cx-w, cy-h, cx+w, cy+h

	cells := map[cell][]member{}
	var order []cell
	m.mu.Lock()
	now := m.now()
	for _, d := range m.candidates(x0, y0, x1, y1, z) {
		if v.PhyMask&(1<<PhyIndex(d.Phy)) == 0 ||
			(v.NewOnly && now-d.First >= NewSecs) || (v.MaxAge > 0 && now-d.Last >= float64(v.MaxAge)) {
			continue
		}
		x, y := WorldPx(d.lat, d.lon, z)
		if !(x0 <= x && x < x1 && y0 <= y && y < y1) {
			continue
		}
		c := cell{int64(math.Floor(x / ClusterPx)), int64(math.Floor(y / ClusterPx))}
		if d.Reported {
			c = cell{math.MinInt64, int64(d.id)}
		}
		if _, ok := cells[c]; !ok {
			order = append(order, c)
		}
		cells[c] = append(cells[c], member{x, y, *d})
	}
	m.mu.Unlock()

	type out struct {
		age        uint8
		n          int
		peak       int
		x, y       float64
		ident      uint32
		phy, flags uint8
		heading    uint8
		icon       uint8
	}
	res := make([]out, 0, len(order))
	for _, c := range order {
		ms := cells[c]
		minAge := math.Inf(1)
		fresh := false
		peak := math.MinInt
		for _, mb := range ms {
			minAge = min(minAge, now-mb.d.Last)
			fresh = fresh || now-mb.d.First < NewSecs
			p := mb.d.Peak
			if p == 0 {
				p = -127
			}
			peak = max(peak, p)
		}
		o := out{age: ageBucket(minAge), n: len(ms), peak: peak}
		if fresh {
			o.flags = FlagNew
		}
		if len(ms) == 1 {
			o.x, o.y, o.ident, o.phy = ms[0].x, ms[0].y, ms[0].d.id, uint8(PhyIndex(ms[0].d.Phy))
			o.icon = ms[0].d.Icon
			if o.icon == IconNone {
				o.icon = IconOf(ms[0].d.Phy, ms[0].d.Type, "", 0)
			}
			if d := ms[0].d; d.Reported {
				o.flags |= FlagReported
				o.heading = uint8(int(math.Round(math.Mod(d.Heading+360, 360)*256/360)) & 0xff)
			}
		} else {
			var counts [PhyOther + 1]int
			for _, mb := range ms {
				o.x += mb.x
				o.y += mb.y
				counts[PhyIndex(mb.d.Phy)]++
			}
			o.x /= float64(len(ms))
			o.y /= float64(len(ms))
			best := 0
			for p, n := range counts {
				if n > counts[best] {
					best = p
				}
			}
			o.ident, o.phy = uint32(len(ms)), uint8(best)
			o.flags |= FlagCluster
		}
		res = append(res, o)
	}

	// most recent first if there are too many
	sort.Slice(res, func(i, j int) bool {
		a, b := res[i], res[j]
		switch {
		case a.age != b.age:
			return a.age < b.age
		case a.n != b.n:
			return a.n > b.n
		case a.peak != b.peak:
			return a.peak > b.peak
		case a.x != b.x:
			return a.x < b.x
		case a.y != b.y:
			return a.y < b.y
		}
		return a.ident < b.ident
	})
	res = res[:min(len(res), MaxMarkers)]

	le := binary.LittleEndian
	buf := le.AppendUint16(make([]byte, 0, HeadSize+len(res)*MarkerSize), uint16(v.Seq))
	buf = append(buf, uint8(z), MarkerSize)
	buf = le.AppendUint16(buf, uint16(len(res)))
	for _, o := range res {
		buf = le.AppendUint32(buf, uint32(o.x))
		buf = le.AppendUint32(buf, uint32(o.y))
		buf = le.AppendUint32(buf, o.ident)
		buf = append(buf, o.phy, o.flags, byte(int8(max(-127, min(0, o.peak)))), o.age, o.heading, o.icon)
	}
	return buf
}

type Detail struct {
	ID      uint32  `json:"id"`
	Name    string  `json:"name"`
	MAC     string  `json:"mac"`
	Phy     string  `json:"phy"`
	Type    string  `json:"type"`
	Channel string  `json:"channel"`
	Signal  int     `json:"signal"`
	Peak    int     `json:"peak"`
	First   string  `json:"first"`
	Last    string  `json:"last"`
	Age     int     `json:"age"`
	Packets int64   `json:"packets"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	// the device's own reported altitude (m), speed (km/h) and heading
	Alt     *float64 `json:"alt,omitempty"`
	Speed   *float64 `json:"speed,omitempty"`
	Heading *float64 `json:"heading,omitempty"`
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func (m *DeviceMap) Detail(id uint32) (Detail, bool) {
	m.mu.Lock()
	p := m.devs[id]
	if p == nil {
		m.mu.Unlock()
		return Detail{}, false
	}
	d := *p
	skew, now := m.skew, m.now()
	m.mu.Unlock()
	name := d.Name
	if name == "" {
		name = d.MAC
	}
	local := func(t float64) string {
		if t == 0 {
			return ""
		}
		return clockStr(t - skew)
	}
	last := d.Last
	if last == 0 {
		last = now
	}
	out := Detail{ID: d.id, Name: name, MAC: d.MAC, Phy: d.Phy, Type: d.Type, Channel: d.Channel,
		Signal: d.Signal, Peak: d.Peak, First: local(d.First), Last: local(d.Last),
		Age: max(0, int(now-last)), Packets: d.Packets, Lat: round6(d.lat), Lon: round6(d.lon)}
	if d.Reported {
		alt, speed, heading := math.Round(d.Alt), math.Round(d.Speed), math.Round(d.Heading)
		out.Alt, out.Speed, out.Heading = &alt, &speed, &heading
	}
	return out, true
}

// TrackLog is our own path as a breadcrumb trail: a point every
// TrackMinStepM, sent as the last TrackSend points, thinned evenly if there
// are more.
type TrackLog struct {
	mu      sync.Mutex
	points  [][2]float64
	version uint32
}

func (t *TrackLog) Add(fix int, lat, lon float64) {
	if fix < 2 || (lat == 0 && lon == 0) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.points); n > 0 {
		p := t.points[n-1]
		dy := (lat - p[0]) * 111320
		dx := (lon - p[1]) * 111320 * math.Cos(lat*math.Pi/180)
		step := math.Hypot(dx, dy)
		if step < TrackMinStepM {
			return
		}
		if step > 2000 { // a jump from a bad fix or a restart: new segment
			t.points = t.points[:0]
		}
	}
	t.points = append(t.points, [2]float64{lat, lon})
	if len(t.points) > TrackKeep {
		t.points = append(t.points[:0], t.points[len(t.points)-TrackKeep:]...)
	}
	t.version++
}

func (t *TrackLog) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.points)
}

func (t *TrackLog) Payload() (uint32, []byte) {
	t.mu.Lock()
	pts := append([][2]float64(nil), t.points...)
	ver := t.version
	t.mu.Unlock()
	if len(pts) > TrackSend {
		step := float64(len(pts)) / TrackSend
		thin := make([][2]float64, 0, TrackSend)
		for i := 0; i < TrackSend-1; i++ {
			thin = append(thin, pts[int(float64(i)*step)])
		}
		pts = append(thin, pts[len(pts)-1])
	}
	le := binary.LittleEndian
	buf := le.AppendUint32(make([]byte, 0, 6+8*len(pts)), ver)
	buf = le.AppendUint16(buf, uint16(len(pts)))
	for _, p := range pts {
		buf = le.AppendUint32(buf, uint32(int32(math.Round(p[0]*1e7))))
		buf = le.AppendUint32(buf, uint32(int32(math.Round(p[1]*1e7))))
	}
	return ver, buf
}
