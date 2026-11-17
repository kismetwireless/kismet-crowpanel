//go:build linux || darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"kismet-crowpanel/link"
)

// --serial auto finds the panel without upsetting other serial devices:
// only ports behind a panel's USB bridge are tried (panelPorts, from device
// metadata, nothing opened), ports another program has open, locked or
// exclusive are skipped, and a port that turns out not to be a panel gets
// its line settings back.  Ports that keep not answering are tried less
// often.

const probeBackoffMax = time.Minute

var errNotPanel = errors.New("no hello")

type portProber struct {
	fails map[string]int
	next  map[string]time.Time
	// probe and portInUse, swapped in tests
	probe func(string, time.Duration) (*os.File, error)
	inUse func(string) string
}

// probe listens on a port for the panel's hello.  The port is handed back
// still open, and exclusive, when it's the panel.
func probe(path string, wait time.Duration) (*os.File, error) {
	f, err := openPort(path)
	if errors.Is(err, syscall.EBUSY) {
		return nil, errors.New("held exclusive by another program")
	}
	if err != nil {
		return nil, err
	}
	if portExclusive(f) {
		f.Close()
		return nil, errors.New("held exclusive by another program")
	}
	saved, err := getTermios(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := setBaud(f, link.LinkBaud); err != nil {
		setTermios(f, saved)
		f.Close()
		return nil, err
	}
	if waitHello(f, wait) {
		setExclusive(f)
		return f, nil
	}
	setTermios(f, saved)
	f.Close()
	return nil, errNotPanel
}

func (pp *portProber) find(ports []string, wait time.Duration) (string, *os.File, error) {
	if pp.fails == nil {
		pp.fails, pp.next = map[string]int{}, map[string]time.Time{}
	}
	if pp.probe == nil {
		pp.probe, pp.inUse = probe, portInUse
	}
	// a port that went away starts over when it's back
	for p := range pp.fails {
		if !slices.Contains(ports, p) {
			delete(pp.fails, p)
			delete(pp.next, p)
		}
	}
	if len(ports) == 0 {
		if h := bridgeHint(); h != "" {
			return "", nil, errors.New(h)
		}
		return "", nil, errors.New("no USB serial ports with a panel's bridge")
	}

	now := time.Now()
	var try, skipped []string
	for _, p := range ports {
		if now.Before(pp.next[p]) {
			skipped = append(skipped, p+": no answer before")
			continue
		}
		if who := pp.inUse(p); who != "" {
			skipped = append(skipped, p+": in use by "+who)
			continue
		}
		try = append(try, p)
	}

	type result struct {
		path string
		f    *os.File
		err  error
	}
	res := make(chan result, len(try))
	for _, p := range try {
		go func() {
			f, err := pp.probe(p, wait)
			res <- result{p, f, err}
		}()
	}
	var found result
	for range try {
		r := <-res
		switch {
		case r.f != nil && found.f == nil:
			found = r
			continue
		case r.f != nil:
			// a second panel: leave it for another client
			r.f.Close()
			continue
		case errors.Is(r.err, errNotPanel):
			n := pp.fails[r.path] + 1
			pp.fails[r.path] = n
			pp.next[r.path] = now.Add(min(wait<<(n-1), probeBackoffMax))
		}
		skipped = append(skipped, r.path+": "+r.err.Error())
	}
	if found.f != nil {
		delete(pp.fails, found.path)
		delete(pp.next, found.path)
		return found.path, found.f, nil
	}
	return "", nil, fmt.Errorf("no panel found (%s)", strings.Join(skipped, "; "))
}
