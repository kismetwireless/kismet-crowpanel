package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"kismet-crowpanel/markers"
)

// Fake drive: a 1.5 km loop around the Royal Observatory, Greenwich
const (
	demoLat     = 51.4769
	demoLon     = -0.0005
	demoRadiusM = 1500
)

var demoPhys = []struct {
	name   string
	weight float64
}{{"802.11", 0.62}, {"BTLE", 0.25}, {"Bluetooth", 0.06}, {"RTL433", 0.04}, {"802.15.4", 0.03}}

var demoTypes = map[string][]string{"802.11": {"Wi-Fi AP", "Wi-Fi Client", "Wi-Fi Bridged"}, "BTLE": {"BTLE"},
	"Bluetooth": {"BR/EDR"}, "RTL433": {"Sensor"}, "802.15.4": {"Zigbee"}}

var demoChannels = strings.Fields("1 1 1 1 1 1 6 6 6 6 6 6 6 6 11 11 11 11 11 11 " +
	"2 3 4 9 36 36 40 44 48 52 100 112 149 149 153 157 161")

var demoAlerts = []struct {
	sev          int
	header, text string
}{
	{10, "DEAUTHFLOOD", "Deauthenticate/Disassociate flood on %s"},
	{15, "APSPOOF", "Unauthorized device advertising SSID 'demo-corp' from %s"},
	{5, "BSSTIMESTAMP", "Out-of-sequence BSS timestamp on %s, possible spoof"},
	{0, "SOURCEERROR", "Data source 'demo-sdr' reported an error%.0s"},
	{20, "WPSBRUTE", "Excessive WPS messages from %s, possible brute force"},
}

type demoDevice struct {
	key, mac, name, phy, typ, channel string
	lat, lon                          float64
	packets                           int64
	base                              int
	first, last                       float64
}

type demoSource struct {
	name, typ, channel string
	hopping            bool
	rate               int
	packets            int64
	hist               ring[int64]
}

// demoPoller makes synthetic data for exercising the UI without a Kismet
// server.  MACs are random locally-administered addresses, names are made
// up.
type demoPoller struct {
	*state
	rng         *rand.Rand
	start       float64
	track       gpsTrack
	angle       float64
	devices     []*demoDevice
	total       int
	alerts      ring[alertDoc]
	alertsTotal int
	sources     []*demoSource
}

func newDemoPoller(s *state) *demoPoller {
	d := &demoPoller{state: s, rng: rand.New(rand.NewPCG(1, 1)), start: nowSecs() - 3*3600 - 1234,
		alerts: ring[alertDoc]{n: maxAlerts}}
	d.sources = []*demoSource{
		{"wlan0", "linuxwifi", "", true, 900, 1200000, ring[int64]{n: histLen}},
		{"wlan1", "linuxwifi", "36", false, 300, 410000, ring[int64]{n: histLen}},
		{"hci0", "linuxbluetooth", "", false, 120, 98000, ring[int64]{n: histLen}},
		{"demo-sdr", "rtl433", "433.920MHz", false, 2, 3100, ring[int64]{n: histLen}},
	}
	// the first devices are spread round the loop, as if driven already
	for i := 0; i < 120; i++ {
		d.angle = 2 * math.Pi * float64(i) / 120
		d.addDevice()
	}
	d.angle = 0
	return d
}

func (d *demoPoller) randInt(lo, hi int) int { return lo + d.rng.IntN(hi-lo+1) }

func (d *demoPoller) randMAC() string {
	b := make([]string, 6)
	for i := range b {
		v := d.rng.IntN(256)
		if i == 0 {
			v = v&0xfc | 0x02
		}
		b[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(b, ":")
}

func (d *demoPoller) position() (float64, float64) {
	lat := demoLat + deg(demoRadiusM*math.Sin(d.angle)/6371000)
	lon := demoLon + deg(demoRadiusM*math.Cos(d.angle)/6371000)/math.Cos(rad(demoLat))
	return lat, lon
}

func (d *demoPoller) addDevice() {
	r := d.rng.Float64()
	phy := demoPhys[len(demoPhys)-1].name
	for _, p := range demoPhys {
		if r < p.weight {
			phy = p.name
			break
		}
		r -= p.weight
	}
	mac := d.randMAC()
	name := ""
	if d.rng.Float64() < 0.5 {
		name = fmt.Sprintf("demo-%s-%s", strings.ReplaceAll(strings.ToLower(phy), ".", ""),
			strings.ToLower(strings.ReplaceAll(mac[len(mac)-5:], ":", "")))
	}
	// around the current position on the fake drive, within ~150 m
	lat, lon := d.position()
	dist := 150 * math.Sqrt(d.rng.Float64())
	a := d.rng.Float64() * 2 * math.Pi
	channel := ""
	if phy == "802.11" {
		channel = demoChannels[d.rng.IntN(len(demoChannels))]
	}
	now := nowSecs()
	first := now
	if d.total < 120 {
		first -= float64(d.randInt(0, 3600))
	}
	types := demoTypes[phy]
	d.devices = append(d.devices, &demoDevice{
		key:     "demo_" + strings.ReplaceAll(mac, ":", ""),
		lat:     lat + deg(dist*math.Sin(a)/6371000),
		lon:     lon + deg(dist*math.Cos(a)/6371000)/math.Cos(rad(lat)),
		packets: int64(d.randInt(1, 5000)),
		mac:     mac, name: name, phy: phy,
		typ:     types[d.rng.IntN(len(types))],
		channel: channel,
		base:    d.randInt(-92, -38),
		last:    now - float64(d.randInt(0, 600)),
		first:   first,
	})
	d.total++
}

func (d *demoPoller) gps(now, dt float64) gpsDoc {
	speed := max(0, 45+35*math.Sin(now/40)) // km/h
	d.angle += speed / 3.6 * dt / demoRadiusM
	lat, lon := d.position()
	heading := pmod(deg(-d.angle), 360)
	d.track.update(3, lat, lon, now)
	d.trail.Add(3, lat, lon)
	return d.track.doc(3, lat, lon, 45, speed, heading)
}

func (d *demoPoller) poll() error {
	now := nowSecs()
	gps := d.gps(now, 2)
	if d.rng.Float64() < 0.35 {
		d.addDevice()
	}
	// devices near us are the ones heard again
	lat, lon := d.position()
	var near []*demoDevice
	for _, dv := range d.devices {
		if haversineM(lat, lon, dv.lat, dv.lon) < 400 {
			near = append(near, dv)
		}
	}
	d.rng.Shuffle(len(near), func(i, j int) { near[i], near[j] = near[j], near[i] })
	for _, dv := range near[:min(25, len(near))] {
		dv.last = now - d.rng.Float64()*2
		dv.packets += int64(d.randInt(1, 50))
	}
	d.devmap.SetClock(now)
	for _, dv := range d.devices {
		d.devmap.Update(dv.key, dv.lat, dv.lon, markers.Info{Name: dv.name, MAC: dv.mac, Phy: dv.phy, Type: dv.typ,
			Channel: dv.channel, Signal: dv.base + d.randInt(-3, 3), Peak: min(-20, dv.base+6),
			First: dv.first, Last: dv.last, Packets: dv.packets})
	}

	burst := 1.0
	if d.rng.Float64() < 0.08 {
		burst += 2.5
	}
	var pps int64
	srcOut := []sourceDoc{}
	for _, s := range d.sources {
		rate := int64(float64(s.rate) * burst * (0.7 + 0.6*d.rng.Float64()))
		s.packets += rate * 2
		pps += rate
		if s.hopping {
			s.channel = []string{"1", "6", "11", "36", "44", "149"}[d.rng.IntN(6)]
		}
		bad := s.name == "demo-sdr" && int64(now/30)%4 == 0
		sp := rate
		if bad {
			sp = 0
		}
		s.hist.push(sp)
		srcOut = append(srcOut, sourceDoc{s.name, s.typ, s.channel, s.packets, sp, !bad, bad, s.hopping,
			s.hist.list()})
	}
	d.ppsHist.push(pps)

	if d.rng.Float64() < 0.06 {
		a := demoAlerts[d.rng.IntN(len(demoAlerts))]
		d.alertsTotal++
		d.alerts.pushFront(alertDoc{Clock: clockStr(time.Now()), Header: a.header, Severity: a.sev,
			Text: fmt.Sprintf(a.text, d.devices[d.rng.IntN(len(d.devices))].mac)})
	}

	var active []*demoDevice
	seen := []seenDevice{}
	nw := 0
	counts := map[string]int{}
	for _, dv := range d.devices {
		if now-dv.last <= activeWindow {
			active = append(active, dv)
			seen = append(seen, seenDevice{dv.phy, dv.channel, dv.last})
		}
		if now-dv.first <= newWindow {
			nw++
		}
		counts[dv.phy]++
	}
	sort.SliceStable(active, func(i, j int) bool { return active[i].base > active[j].base })
	top := []topDoc{}
	for _, dv := range active[:min(len(active), maxDevices)] {
		top = append(top, topDoc{dv.name, dv.mac, dv.phy, dv.typ, dv.channel, dv.base + d.randInt(-3, 3),
			int(now - dv.last)})
	}
	phys := []phyDoc{}
	for k, v := range counts {
		phys = append(phys, phyDoc{k, v})
	}
	sort.Slice(phys, func(i, j int) bool {
		return phys[i].Count > phys[j].Count || phys[i].Count == phys[j].Count && phys[i].Name < phys[j].Name
	})

	d.publish(stateDoc{
		V: 1, Link: "ok", Server: d.name, Clock: clockStr(time.Now()), Uptime: int64(now - d.start),
		Sun: d.track.sun, GPS: gps, Channels: channelCounts(seen, now),
		Devices: devicesDoc{Total: d.total, Active: len(active), New: nw, Phys: phys},
		PPS:     pps, PPSHist: d.ppsHist.list(), Sources: srcOut, Top: top,
		Alerts: alertsDoc{Total: d.alertsTotal, Recent: d.alerts.list()},
	})
	return nil
}
