package markers

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"testing"
)

const lat0, lon0 = 51.4769, -0.0005

type mk struct {
	x, y, id   uint32
	phy, flags uint8
	peak       int8
	age        uint8
	heading    uint8
	kind       uint8
}

func decode(t *testing.T, b []byte) (seq uint16, z uint8, ms []mk) {
	t.Helper()
	le := binary.LittleEndian
	n := int(le.Uint16(b[4:]))
	if len(b) != HeadSize+n*MarkerSize || b[3] != MarkerSize {
		t.Fatalf("payload is %d bytes for %d markers", len(b), n)
	}
	for i := 0; i < n; i++ {
		p := b[HeadSize+i*MarkerSize:]
		ms = append(ms, mk{le.Uint32(p), le.Uint32(p[4:]), le.Uint32(p[8:]), p[12], p[13], int8(p[14]), p[15], p[16], p[17]})
	}
	return le.Uint16(b), b[2], ms
}

func offset(dx, dy float64) (float64, float64) {
	return lat0 + dy/6371000*180/math.Pi, lon0 + dx/6371000*180/math.Pi/math.Cos(lat0*math.Pi/180)
}

func view(z int) View {
	return View{Seq: 3, Z: z, Lat: lat0, Lon: lon0, W: 736, H: 448, PhyMask: PhyAll}
}

func TestMarkers(t *testing.T) {
	now := 1_700_000_000.0
	dm := NewDeviceMap()
	dm.Now = func() float64 { return now }
	dm.SetClock(now)
	add := func(key string, dx, dy float64, info Info) {
		la, lo := offset(dx, dy)
		dm.Update(key, la, lo, info)
	}
	add("a", 100, 0, Info{Phy: "IEEE802.11", First: now - 3000, Last: now - 10, Peak: -55, MAC: "02:00:00:00:00:0a"})
	add("b1", -200, 50, Info{Phy: "BTLE", First: now - 3000, Last: now - 900, Peak: -80})
	add("b2", -201, 51, Info{Phy: "BTLE", First: now - 3000, Last: now - 2000, Peak: -70})
	add("n", 0, -150, Info{Phy: "RTL433", First: now - 5, Last: now - 5, Peak: -60})
	add("far", 30000, 0, Info{Phy: "IEEE802.11", First: now - 100, Last: now - 100})

	v := view(17)
	v.Seq = 7
	seq, z, ms := decode(t, dm.Markers(v))
	if seq != 7 || z != 17 {
		t.Errorf("header seq %d z %d", seq, z)
	}
	if len(ms) != 3 {
		t.Fatalf("want 3 markers (nearby only, two clustered), got %d", len(ms))
	}
	by := map[uint8]mk{}
	for _, m := range ms {
		by[m.flags] = m
	}
	if c, ok := by[FlagCluster]; !ok || c.id != 2 || c.phy != PhyBTLE || c.peak != -70 || c.age != 2 {
		t.Errorf("cluster carries count, phy, best peak, freshest age: %+v", c)
	}
	if n, ok := by[FlagNew]; !ok || n.phy != PhySDR {
		t.Errorf("a device first seen under a minute ago is new: %+v", n)
	}
	cx, cy := WorldPx(lat0, lon0, 17)
	if s, ok := by[0]; !ok || float64(s.x)-cx <= 0 || float64(s.x)-cx >= 200 || math.Abs(float64(s.y)-cy) >= 2 {
		t.Errorf("device placed at its location in world px: %+v", s)
	}

	_, _, far := decode(t, dm.Markers(view(8)))
	var got []string
	for _, m := range far {
		n := uint32(1)
		if m.flags&FlagCluster != 0 {
			n = m.id
		}
		got = append(got, fmt.Sprint(m.flags&FlagCluster, n))
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint([]string{"0 1", fmt.Sprint(FlagCluster, 4)}) {
		t.Errorf("zoomed out, the nearby four are one cluster: %v", got)
	}

	count := func(f func(*View)) int {
		v := view(17)
		f(&v)
		_, _, ms := decode(t, dm.Markers(v))
		return len(ms)
	}
	if n := count(func(v *View) { v.PhyMask = 1 << PhyWifi }); n != 1 {
		t.Errorf("phy filter keeps only that phy: %d", n)
	}
	if n := count(func(v *View) { v.PhyMask = PhyAll &^ (1 << PhyBTLE) }); n != 2 {
		t.Errorf("phy filter drops a phy: %d", n)
	}
	if n := count(func(v *View) { v.NewOnly = true }); n != 1 {
		t.Errorf("new filter: %d", n)
	}
	if n := count(func(v *View) { v.MaxAge = RecentSecs }); n != 2 {
		t.Errorf("recent filter: %d", n)
	}
	if n := count(func(v *View) { v.MaxAge = 3600 }); n != 3 {
		t.Errorf("hour filter: %d", n)
	}

	q := map[string]string{"z": "15", "lat": "1.5", "lon": "2", "w": "10", "h": "20", "phy": "255", "recent": "1"}
	va, err := ViewFrom(func(k string) (string, bool) { s, ok := q[k]; return s, ok })
	if err != nil || va.PhyMask != PhyAll || va.MaxAge != RecentSecs || va.NewOnly || va.Seq != 0 || va.Z != 15 {
		t.Errorf("view fields from a query string, with defaults: %+v %v", va, err)
	}
	delete(q, "lat")
	if _, err := ViewFrom(func(k string) (string, bool) { s, ok := q[k]; return s, ok }); err == nil {
		t.Error("a view without lat is refused")
	}

	add("a", 5000, 0, Info{Phy: "IEEE802.11", First: now - 3000, Last: now - 10, Peak: -50, MAC: "02:00:00:00:00:0a"})
	if _, _, ms := decode(t, dm.Markers(view(17))); len(ms) != 2 {
		t.Errorf("a device that moved leaves the old spot: %d", len(ms))
	}

	id, _ := dm.ID("a")
	d, ok := dm.Detail(id)
	if !ok || d.MAC != "02:00:00:00:00:0a" || d.Peak != -50 || d.Age != 10 || d.Name != d.MAC {
		t.Errorf("detail: %+v", d)
	}
	if _, ok := dm.Detail(9999); ok {
		t.Error("no detail for an unknown id")
	}
}

func TestMarkerCap(t *testing.T) {
	now := 1_700_000_000.0
	big := NewDeviceMap()
	big.Now = func() float64 { return now }
	big.SetClock(now)
	for i := 0; i < 1000; i++ {
		// 11 m apart: about 30 px at z18, so no two share a 24 px cluster cell
		la, lo := offset(float64(i%40)*11-220, float64(i/40)*11-137)
		big.Update(fmt.Sprint("d", i), la, lo, Info{Phy: "BTLE", First: now - 4000, Last: now - float64(i%7)*600})
	}
	_, _, many := decode(t, big.Markers(view(18)))
	if len(many) != MaxMarkers {
		t.Fatalf("capped at MaxMarkers: %d", len(many))
	}
	lo, hi, fresh := uint8(255), uint8(0), 0
	for _, m := range many {
		lo, hi = min(lo, m.age), max(hi, m.age)
		if m.age == 0 {
			fresh++
		}
	}
	if hi > lo+3 || fresh < 100 {
		t.Errorf("the most recently seen are kept: %d fresh", fresh)
	}
}

func TestHelpers(t *testing.T) {
	if _, _, ok := Geopoint([]any{0.0, 0.0}); ok {
		t.Error("zero means no location")
	}
	if _, _, ok := Geopoint(nil); ok {
		t.Error("nil means no location")
	}
	if la, lo, ok := Geopoint([]any{-0.1, 51.5}); !ok || la != 51.5 || lo != -0.1 {
		t.Error("Kismet points are [lon, lat]")
	}
	if PhyIndex("IEEE802.11") != PhyWifi || PhyIndex("Bluetooth") != PhyBT || PhyIndex("Zigbee") != PhyOther {
		t.Error("phy names map to the panel's colours")
	}
}

func TestTrack(t *testing.T) {
	var tl TrackLog
	tl.Add(3, lat0, lon0)
	la, lo := offset(5, 0)
	tl.Add(3, la, lo)
	if tl.Len() != 1 {
		t.Error("track ignores steps under 15 m")
	}
	for i := 1; i < 1000; i++ {
		la, lo := offset(0, float64(i*20))
		tl.Add(3, la, lo)
	}
	ver, buf := tl.Payload()
	le := binary.LittleEndian
	n := int(le.Uint16(buf[4:]))
	last := int32(le.Uint32(buf[6+(n-1)*8:]))
	want, _ := offset(0, 999*20)
	if n != TrackSend || le.Uint32(buf) != ver || math.Abs(float64(last)/1e7-want) > 1e-6 || len(buf) != 6+8*n {
		t.Errorf("track thinned to TrackSend points, ending where we are: n %d", n)
	}
	la, lo = offset(0, 999*20+5000)
	tl.Add(3, la, lo)
	if tl.Len() != 1 {
		t.Error("a jump starts a new track")
	}
	tl.Add(1, lat0, lon0)
	if tl.Len() != 1 {
		t.Error("no track points without a fix")
	}
}

// Aircraft and vessels sit at their own position, alone, with a heading
func TestReported(t *testing.T) {
	now := 1_700_000_000.0
	dm := NewDeviceMap()
	dm.Now = func() float64 { return now }
	dm.SetClock(now)
	for i, key := range []string{"w1", "w2"} {
		la, lo := offset(float64(i), 0)
		dm.Update(key, la, lo, Info{Phy: "IEEE802.11", First: now - 100, Last: now - 10})
	}
	la, lo := offset(1, 1)
	dm.Update("plane", la, lo, Info{Phy: "ADSB", First: now - 100, Last: now - 10,
		Reported: true, Heading: 90, Alt: 4755, Speed: 648})
	la, lo = offset(-1, 1)
	dm.Update("ship", la, lo, Info{Phy: "AIS", First: now - 100, Last: now - 10, Reported: true, Heading: 359.9})

	_, _, ms := decode(t, dm.Markers(view(15)))
	var cl, plane, ship *mk
	for i := range ms {
		m := &ms[i]
		switch {
		case m.flags&FlagCluster != 0:
			cl = m
		case m.kind == IconPlane:
			plane = m
		case m.kind == IconVessel:
			ship = m
		}
	}
	if cl == nil || cl.id != 2 || len(ms) != 3 {
		t.Fatalf("want a 2-device cluster plus two reported markers, got %+v", ms)
	}
	if plane.flags&FlagReported == 0 || plane.heading != 64 || plane.phy != PhySDR {
		t.Errorf("aircraft marker %+v", *plane)
	}
	if ship.flags&FlagReported == 0 || ship.heading != 0 {
		t.Errorf("vessel marker %+v", *ship)
	}
	d, _ := dm.Detail(plane.id)
	if d.Alt == nil || *d.Alt != 4755 || *d.Speed != 648 || *d.Heading != 90 {
		t.Errorf("aircraft detail %+v", d)
	}
}

func TestIcons(t *testing.T) {
	cases := []struct {
		phy, typ, ais string
		uav           int
		want          uint8
	}{
		{"IEEE802.11", "Wi-Fi AP", "", 0, IconWifi},
		{"BTLE", "BTLE", "", 0, IconBluetooth},
		{"ADSB", "Airplane", "", 0, IconPlane},
		{"ADSB", "Helicopter", "", 0, IconHelicopter},
		{"ADSB", "Gyroplane", "", 0, IconHelicopter},
		{"AIS", "", "Class A", 0, IconVessel},
		{"AIS", "", "Base Station", 0, IconTower},
		{"AIS", "", "AIS-EPIRB", 0, IconLifebuoy},
		{"IEEE802.11", "Wi-Fi Client", "", 2, IconDrone},
		{"RFSENSOR", "", "", 0, IconGauge},
		{"Z-Wave", "", "", 0, IconRadio},
	}
	for _, c := range cases {
		if got := IconOf(c.phy, c.typ, c.ais, c.uav); got != c.want {
			t.Errorf("%s/%s/%s/%d: icon %d, want %d", c.phy, c.typ, c.ais, c.uav, got, c.want)
		}
	}
}
