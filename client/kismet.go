package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"kismet-crowpanel/markers"
)

const (
	markerInterval      = 5 * time.Second
	mapSettingsInterval = 60 * time.Second
)

var deviceFields = [][2]string{
	{"kismet.device.base.macaddr", "mac"},
	{"kismet.device.base.commonname", "name"},
	{"kismet.device.base.phyname", "phy"},
	{"kismet.device.base.type", "type"},
	{"kismet.device.base.channel", "channel"},
	{"kismet.device.base.last_time", "last"},
	{"kismet.device.base.first_time", "first"},
	{"kismet.device.base.signal/kismet.common.signal.last_signal", "signal"},
}

// located devices for the map: the position a device reports for itself
// (ADS-B, AIS), else where it was heard best, else the average
var markerFields = [][2]string{
	{"kismet.device.base.key", "key"},
	{"kismet.device.base.macaddr", "mac"},
	{"kismet.device.base.commonname", "name"},
	{"kismet.device.base.phyname", "phy"},
	{"kismet.device.base.type", "type"},
	{"kismet.device.base.channel", "channel"},
	{"kismet.device.base.first_time", "first"},
	{"kismet.device.base.last_time", "last"},
	{"kismet.device.base.packets.total", "packets"},
	{"kismet.device.base.signal/kismet.common.signal.last_signal", "signal"},
	{"kismet.device.base.signal/kismet.common.signal.max_signal", "peak"},
	{"kismet.device.base.signal/kismet.common.signal.peak_loc/kismet.common.location.geopoint", "peak_geo"},
	{"kismet.device.base.location/kismet.common.location.avg_loc/kismet.common.location.geopoint", "avg_geo"},
	{"kismet.device.base.reported_location/kismet.common.location.last/kismet.common.location.geopoint", "rep_geo"},
	{"kismet.device.base.reported_location/kismet.common.location.last/kismet.common.location.heading", "rep_heading"},
	{"kismet.device.base.reported_location/kismet.common.location.last/kismet.common.location.alt", "rep_alt"},
	{"kismet.device.base.reported_location/kismet.common.location.last/kismet.common.location.speed", "rep_speed"},
	{"ais.device/ais.device.class", "ais_class"},
	{"uav.device/uav.match_type", "uav_match"},
}

type kismetAPI struct {
	base     string
	apikey   string
	user     string
	password string
	client   *http.Client
}

func newKismetAPI(base, apikey, user, password string) *kismetAPI {
	return &kismetAPI{strings.TrimRight(base, "/"), apikey, user, password, &http.Client{Timeout: 5 * time.Second}}
}

func (k *kismetAPI) req(path string, body io.Reader) (any, error) {
	method := "GET"
	if body != nil {
		method = "POST"
	}
	r, err := http.NewRequest(method, k.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("kismet: %v", err)
	}
	r.Header.Set("Accept", "application/json")
	if body != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if k.apikey != "" {
		r.Header.Set("Cookie", "KISMET="+k.apikey)
	} else if k.user != "" {
		r.SetBasicAuth(k.user, k.password)
	}
	resp, err := k.client.Do(r)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("kismet: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, errors.New("kismet: auth failed")
	}
	if resp.StatusCode >= 400 {
		return nil, &httpError{resp.StatusCode, path}
	}
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("kismet: bad JSON from %s", path)
	}
	return v, nil
}

type httpError struct {
	code int
	path string
}

func (e *httpError) Error() string { return fmt.Sprintf("kismet: HTTP %d on %s", e.code, e.path) }

func (k *kismetAPI) get(path string) (any, error) { return k.req(path, nil) }

func (k *kismetAPI) post(path string, payload any) (any, error) {
	b, _ := json.Marshal(payload)
	return k.req(path, strings.NewReader(url.Values{"json": {string(b)}}.Encode()))
}

func (k *kismetAPI) getObj(path string) (map[string]any, error) {
	v, err := k.get(path)
	return obj(v), err
}

func (k *kismetAPI) list(path string, fields [][2]string) ([]map[string]any, error) {
	v, err := k.post(path, map[string]any{"fields": fields})
	return objs(v), err
}

type kismetPoller struct {
	*state
	api         *kismetAPI
	alertTS     float64
	alerts      ring[alertDoc]
	alertsTotal int
	lastPkts    *pktSample
	srcHist     map[string]*ring[int64] // by source uuid
	track       gpsTrack
	mapPolled   time.Time
	markerPoll  time.Time
	markerTS    int64 // Kismet's clock; 1 = everything on the first poll
	noModified  bool  // Kismet predates /devices/modified-since/
}

type pktSample struct {
	at     time.Time
	total  int64
	perSrc map[string]int64
}

func newKismetPoller(s *state, api *kismetAPI) *kismetPoller {
	return &kismetPoller{state: s, api: api, alerts: ring[alertDoc]{n: maxAlerts}, markerTS: 1,
		srcHist: map[string]*ring[int64]{}}
}

func (k *kismetPoller) pollMapSettings() {
	if k.tiles == nil || time.Since(k.mapPolled) < mapSettingsInterval {
		return
	}
	k.mapPolled = time.Now()
	doc, err := k.api.getObj("/ui/map_tiles.json")
	if err != nil {
		log.Printf("WARNING map tile setting: %v", err)
		return
	}
	k.tiles.SetSettings(doc)
}

// pollMarkers fetches the devices changed since the last poll.  The cursor
// is Kismet's own clock at the poll: a device's last_time is its packets'
// time, which a capture source with a skewed clock can put in the future,
// and a cursor taken from it would then skip every device seen after.
func (k *kismetPoller) pollMarkers(now float64) {
	if time.Since(k.markerPoll) < markerInterval {
		return
	}
	k.markerPoll = time.Now()
	devs, err := k.changedDevices(k.markerTS)
	if err != nil {
		log.Printf("WARNING map devices: %v", err)
		return
	}
	// a little overlap for devices updated during the poll
	k.markerTS = max(k.markerTS, int64(now)-2)
	for _, d := range devs {
		lat, lon, reported := markers.Geopoint(d["rep_geo"])
		ok := reported
		// an aircraft or vessel heard but not yet placed would land on us
		if !ok && !markers.ReportsPosition(str(d, "phy")) {
			lat, lon, ok = markers.Geopoint(d["peak_geo"])
			if !ok {
				lat, lon, ok = markers.Geopoint(d["avg_geo"])
			}
		}
		if !ok || str(d, "key") == "" {
			continue
		}
		info := markers.Info{
			Name: str(d, "name"), MAC: str(d, "mac"), Phy: str(d, "phy"), Type: str(d, "type"),
			Channel: str(d, "channel"), Signal: int(num(d, "signal")), Peak: int(num(d, "peak")),
			First: num(d, "first"), Last: num(d, "last"), Packets: int64(num(d, "packets")),
			Icon: markers.IconOf(str(d, "phy"), str(d, "type"), str(d, "ais_class"), int(num(d, "uav_match")))}
		if reported {
			info.Reported = true
			info.Heading, info.Alt, info.Speed = num(d, "rep_heading"), num(d, "rep_alt"), num(d, "rep_speed")
		}
		k.devmap.Update(str(d, "key"), lat, lon, info)
	}
}

// changedDevices asks for devices modified (any change, on Kismet's clock)
// after ts, or on older Kismet, seen (by packet time) after it
func (k *kismetPoller) changedDevices(ts int64) ([]map[string]any, error) {
	if !k.noModified {
		devs, err := k.api.list(fmt.Sprintf("/devices/modified-since/%d/devices.json", ts), markerFields)
		var he *httpError
		if !errors.As(err, &he) || he.code != http.StatusNotFound {
			return devs, err
		}
		k.noModified = true
		log.Printf("INFO kismet has no /devices/modified-since/; using last-time for map devices")
	}
	return k.api.list(fmt.Sprintf("/devices/last-time/%d/devices.json", ts), markerFields)
}

func (k *kismetPoller) pollGPS(now float64) gpsDoc {
	loc, err := k.api.getObj("/gps/location.json")
	if err != nil {
		loc = map[string]any{}
	}
	fix := int(num(loc, "kismet.common.location.fix"))
	var lat, lon float64
	if p, _ := loc["kismet.common.location.geopoint"].([]any); len(p) >= 2 {
		lon, _ = p[0].(float64)
		lat, _ = p[1].(float64)
	}
	k.track.update(fix, lat, lon, now)
	k.trail.Add(fix, lat, lon)
	return k.track.doc(fix, lat, lon, num(loc, "kismet.common.location.alt"),
		num(loc, "kismet.common.location.speed"), num(loc, "kismet.common.location.heading"))
}

func (k *kismetPoller) pollAlerts() error {
	// the unwrapped route returns a bare list without the server timestamp
	res, err := k.api.getObj("/alerts/wrapped/last-time/" + strconv.FormatFloat(k.alertTS, 'f', -1, 64) + "/alerts.json")
	if err != nil {
		return err
	}
	if _, ok := res["kismet.alert.timestamp"]; ok {
		k.alertTS = num(res, "kismet.alert.timestamp")
	}
	list := objs(res["kismet.alert.list"])
	sort.SliceStable(list, func(i, j int) bool {
		return num(list[i], "kismet.alert.timestamp") < num(list[j], "kismet.alert.timestamp")
	})
	for _, a := range list {
		k.alertsTotal++
		header := str(a, "kismet.alert.header")
		if _, ok := a["kismet.alert.header"]; !ok {
			header = "?"
		}
		k.alerts.pushFront(alertDoc{
			Clock:    clockStr(unixTime(num(a, "kismet.alert.timestamp"))),
			Header:   clip(header, 32),
			Text:     clip(str(a, "kismet.alert.text"), 112),
			Severity: int(num(a, "kismet.alert.severity")),
		})
	}
	return nil
}

func (k *kismetPoller) poll() error {
	status, err := k.api.getObj("/system/status.json")
	if err != nil {
		return err
	}
	srcV, err := k.api.get("/datasource/all_sources.json")
	if err != nil {
		return err
	}
	physV, err := k.api.get("/phy/all_phys.json")
	if err != nil {
		return err
	}
	recent, err := k.api.list(fmt.Sprintf("/devices/last-time/-%d/devices.json", newWindow), deviceFields)
	if err != nil {
		return err
	}
	if err := k.pollAlerts(); err != nil {
		return err
	}
	k.pollMapSettings()

	nowM := time.Now()
	// device times are on Kismet's clock
	now := num(status, "kismet.system.timestamp.sec")
	if now == 0 {
		now = nowSecs()
	}
	k.devmap.SetClock(now)
	k.pollMarkers(now)

	var active []map[string]any
	nw := 0
	for _, d := range recent {
		if now-num(d, "last") <= activeWindow {
			active = append(active, d)
		}
		if now-num(d, "first") <= newWindow {
			nw++
		}
	}
	gps := k.pollGPS(nowSecs())

	sources := objs(srcV)
	uuid := func(i int, s map[string]any) string {
		if _, ok := s["kismet.datasource.uuid"]; ok {
			return str(s, "kismet.datasource.uuid")
		}
		return strconv.Itoa(i)
	}
	perSrc := map[string]int64{}
	var total int64
	for i, s := range sources {
		n := int64(num(s, "kismet.datasource.num_packets"))
		perSrc[uuid(i, s)] = n
		total += n
	}
	var pps int64
	srcPPS := map[string]int64{}
	if p := k.lastPkts; p != nil {
		dt := max(0.001, nowM.Sub(p.at).Seconds())
		pps = max(0, int64(math.Round(float64(total-p.total)/dt)))
		for u, n := range perSrc {
			prev, ok := p.perSrc[u]
			if !ok {
				prev = n
			}
			srcPPS[u] = max(0, int64(math.Round(float64(n-prev)/dt)))
		}
	}
	k.lastPkts = &pktSample{nowM, total, perSrc}
	k.ppsHist.push(pps)
	for u := range k.srcHist {
		if _, ok := perSrc[u]; !ok {
			delete(k.srcHist, u)
		}
	}
	for u := range perSrc {
		h := k.srcHist[u]
		if h == nil {
			h = &ring[int64]{n: histLen}
			k.srcHist[u] = h
		}
		h.push(srcPPS[u])
	}

	srcOut := []sourceDoc{}
	for i, s := range sources[:min(len(sources), maxSources)] {
		u := uuid(i, s)
		drv := obj(s["kismet.datasource.type_driver"])
		name := str(s, "kismet.datasource.name")
		if _, ok := s["kismet.datasource.name"]; !ok {
			name = "?"
		}
		srcOut = append(srcOut, sourceDoc{
			Name:    clip(name, 32),
			Type:    clip(str(drv, "kismet.datasource.driver.type"), 20),
			Channel: clip(str(s, "kismet.datasource.channel"), 16),
			Packets: perSrc[u],
			PPS:     srcPPS[u],
			Running: num(s, "kismet.datasource.running") != 0,
			Error:   num(s, "kismet.datasource.error") != 0,
			Hopping: num(s, "kismet.datasource.hopping") != 0,
			Hist:    k.srcHist[u].list(),
		})
	}

	physOut := []phyDoc{}
	for _, p := range objs(physV) {
		name := str(p, "kismet.phy.phy_name")
		if _, ok := p["kismet.phy.phy_name"]; !ok {
			name = "?"
		}
		if n := int(num(p, "kismet.phy.device_count")); n > 0 {
			physOut = append(physOut, phyDoc{clip(shortPhy(name), 24), n})
		}
	}
	sort.SliceStable(physOut, func(i, j int) bool { return physOut[i].Count > physOut[j].Count })
	physOut = physOut[:min(len(physOut), maxPhys)]

	// strongest first; devices without a signal reading sort last
	sort.SliceStable(active, func(i, j int) bool {
		a, b := num(active[i], "signal"), num(active[j], "signal")
		if (a == 0) != (b == 0) {
			return b == 0
		}
		if a != b {
			return a > b
		}
		return num(active[i], "last") > num(active[j], "last")
	})
	top := []topDoc{}
	seen := make([]seenDevice, 0, len(active))
	for i, d := range active {
		seen = append(seen, seenDevice{str(d, "phy"), str(d, "channel"), num(d, "last")})
		if i >= maxDevices {
			continue
		}
		mac, name := str(d, "mac"), str(d, "name")
		if name == mac {
			name = ""
		}
		last := now
		if _, ok := d["last"]; ok {
			last = num(d, "last")
		}
		top = append(top, topDoc{
			Name:    clip(name, 40),
			MAC:     mac[:min(len(mac), 17)],
			Phy:     clip(shortPhy(str(d, "phy")), 20),
			Type:    clip(str(d, "type"), 24),
			Channel: clip(str(d, "channel"), 12),
			Signal:  int(num(d, "signal")),
			Age:     max(0, int(now-last)),
		})
	}

	var uptime int64
	if start := num(status, "kismet.system.timestamp.start_sec"); start != 0 {
		uptime = max(0, int64(now-start))
	}
	server := str(status, "kismet.system.server_name")
	if server == "" {
		server = k.name
	}

	k.publish(stateDoc{
		V: 1, Link: "ok", Server: clip(server, 32), Clock: clockStr(time.Now()), Uptime: uptime,
		Sun: k.track.sun, GPS: gps,
		Devices: devicesDoc{Total: int(num(status, "kismet.system.devices.count")), Active: len(active),
			New: nw, Phys: physOut},
		Channels: channelCounts(seen, now),
		PPS:      pps,
		PPSHist:  k.ppsHist.list(),
		Sources:  srcOut,
		Top:      top,
		Alerts:   alertsDoc{Total: k.alertsTotal, Recent: k.alerts.list()},
	})
	return nil
}
