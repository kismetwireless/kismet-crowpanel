//go:build linux || darwin

package main

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProberSkipsAndBacksOff(t *testing.T) {
	var mu sync.Mutex
	probed := map[string]int{}
	panel, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	present := false
	pp := &portProber{
		inUse: func(p string) string {
			if p == "/dev/gps" {
				return "gpsd[100]"
			}
			return ""
		},
		probe: func(p string, _ time.Duration) (*os.File, error) {
			mu.Lock()
			probed[p]++
			mu.Unlock()
			if p == "/dev/panel" && present {
				return panel, nil
			}
			return nil, errNotPanel
		},
	}
	ports := []string{"/dev/gps", "/dev/other", "/dev/panel"}
	_, _, err = pp.find(ports, time.Second)
	if err == nil || !strings.Contains(err.Error(), "in use by gpsd[100]") {
		t.Errorf("want the in-use port named: %v", err)
	}
	if probed["/dev/gps"] != 0 {
		t.Error("a port in use was opened")
	}
	// both failed a moment ago, so they wait
	pp.find(ports, time.Second)
	if probed["/dev/other"] != 1 || probed["/dev/panel"] != 1 {
		t.Errorf("retried too soon: %v", probed)
	}
	// unplugged and back: tried again at once
	pp.find(ports[:2], time.Second)
	present = true
	path, f, err := pp.find(ports, time.Second)
	if err != nil || path != "/dev/panel" || f != panel {
		t.Errorf("found %q, %v", path, err)
	}
	if probed["/dev/other"] != 1 {
		t.Errorf("probed %v", probed)
	}
	f.Close()
}

func TestUnGarble(t *testing.T) {
	for in, want := range map[string]string{
		"ui: frame took 101 ms\r":                         "ui: frame took 101 ms",
		"''\uFFFD!\uFFFD1!\uFFFDq\x01ui: frame took 7 ms": "ui: frame took 7 ms",
		"\uFFFD \uFFFD":                                   "",
	} {
		if got := unGarble(in); got != want {
			t.Errorf("unGarble(%q) = %q", in, got)
		}
	}
}
