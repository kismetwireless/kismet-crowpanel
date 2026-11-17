package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDevices serves device lists like Kismet: devices whose time (mod or
// last) is after the timestamp in the path
type fakeDevices struct {
	mu       sync.Mutex
	modified bool // has /devices/modified-since/
	devs     []map[string]any
	asked    []string
}

func (f *fakeDevices) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	route, field := "/devices/last-time/", "last"
	if f.modified {
		route, field = "/devices/modified-since/", "mod"
	}
	rest, ok := strings.CutPrefix(r.URL.Path, route)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f.asked = append(f.asked, r.URL.Path)
	ts, _ := strconv.ParseFloat(strings.TrimSuffix(rest, "/devices.json"), 64)
	out := []map[string]any{}
	for _, d := range f.devs {
		if d[field].(float64) > ts {
			out = append(out, d)
		}
	}
	json.NewEncoder(w).Encode(out)
}

func (f *fakeDevices) add(key string, last, mod float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs = append(f.devs, map[string]any{"key": key, "phy": "IEEE802.11", "last": last, "mod": mod,
		"peak_geo": []any{-73.9, 40.7}})
}

func markerPollWith(t *testing.T, modified bool) (*fakeDevices, *kismetPoller) {
	f := &fakeDevices{modified: modified}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, newKismetPoller(newState("test"), newKismetAPI(srv.URL, "", "", ""))
}

// A device stamped in the future by a skewed capture source mustn't hide
// the devices seen after it
func TestMarkersFutureDevice(t *testing.T) {
	for _, modified := range []bool{true, false} {
		f, k := markerPollWith(t, modified)
		now := 1_000_000.0
		f.add("skewed", now+3600, now)
		k.pollMarkers(now)
		f.add("new", now+10, now+10)
		k.markerPoll = time.Time{}
		k.pollMarkers(now + 10)
		if _, ok := k.devmap.ID("new"); !ok || k.devmap.Len() != 2 {
			t.Errorf("modified-since %v: %d devices, new one missing; asked %v", modified, k.devmap.Len(), f.asked)
		}
		if want := "/devices/modified-since/"; modified != strings.HasPrefix(f.asked[len(f.asked)-1], want) {
			t.Errorf("modified-since %v: asked %v", modified, f.asked)
		}
	}
}
