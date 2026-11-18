package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kismet-crowpanel/link"
	"kismet-crowpanel/markers"
	"kismet-crowpanel/tiles"
)

const (
	serialTileWorkers = 4
	serialTileQueue   = 8
	serialWriteWait   = 2 * time.Second
	// --serial auto: look for the panel on every CH340 port
	serialAuto = "auto"
)

// Link rate timing, variables for the tests: how long the panel has to
// answer a rate request, to get a frame through at the new rate, and to stay
// quiet at a fast rate before the client goes back to link.LinkBaud (the
// panel hellos every 5 s once linked, and drops back after 8 s of quiet).
var (
	baudReplyWait   = 2 * time.Second
	baudConfirmWait = 3 * time.Second
	linkQuietWait   = 12 * time.Second
	probeWait       = 3 * time.Second
	// frames that fail their CRC: this many in linkBadWindow at a fast
	// rate means the rate doesn't really work
	linkBadMax    = 3
	linkBadWindow = 30 * time.Second
)

// faster rates the firmware takes, tried from the one asked for on down;
// --baud auto starts at the top
var linkRates = []int{2000000, 1500000, 921600, 460800, 230400}

// serialLink pushes the state document to the panel over a serial port,
// and answers its hello, view, tile, detail and consent frames.  Tiles are
// fetched on workers so a slow tile server doesn't hold up the state.
// Reopens the port if it goes away (unplug, reflash).
//
// The panel starts at link.LinkBaud and hellos; the client then moves the
// link to baud, or the fastest slower rate that works.
type serialLink struct {
	st       *state
	path     string // or serialAuto
	baud     int
	interval time.Duration
	failed   map[int]bool // rates that didn't work this run
	prober   *portProber
}

// serialPort is the open port, or a pipe in tests
type serialPort interface {
	io.ReadWriter
	SetWriteDeadline(time.Time) error
}

// session is one open port
type session struct {
	*serialLink
	f         serialPort
	setRate   func(int) error
	wmu       sync.Mutex
	rate      int // under wmu
	tileJobs  chan link.TileRequest
	pendMu    sync.Mutex
	pending   int
	trackSent int64

	// session goroutine only
	ready      bool // the panel has said hello
	asked      int  // rate asked for, waiting for the panel's answer
	askedAt    time.Time
	confirming bool // switched, nothing heard at the new rate yet
	switchedAt time.Time
	lastFrame  time.Time
	hello      string
	badBase    int // the decoder's bad frame count when badSince began
	badSince   time.Time
}

func (s *session) send(t link.Type, payload []byte) error {
	b := link.Encode(t, payload)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// a big state document takes seconds at 115200
	s.f.SetWriteDeadline(time.Now().Add(serialWriteWait + time.Duration(len(b))*10*time.Second/time.Duration(s.rate)))
	_, err := s.f.Write(b)
	return err
}

// switchRate changes the port's rate once what's written has gone out
func (s *session) switchRate(rate int) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := s.setRate(rate); err != nil {
		return err
	}
	s.rate = rate
	return nil
}

// nextRate is the rate to ask the panel for: baud, or the fastest slower
// one that hasn't failed; link.LinkBaud when there's none
func (l *serialLink) nextRate() int {
	if l.baud > link.LinkBaud && !l.failed[l.baud] {
		return l.baud
	}
	for _, r := range linkRates {
		if r < l.baud && r > link.LinkBaud && baudOK(r) && !l.failed[r] {
			return r
		}
	}
	return link.LinkBaud
}

// onHello reports whether to push the state now.  A hello at the starting
// rate from firmware that names its rate starts the move to a faster one.
func (s *session) onHello(p []byte) (bool, error) {
	if h := strings.ToValidUTF8(string(p), "�"); h != s.hello {
		log.Printf("INFO panel hello: %s", h)
		s.hello = h
	}
	s.ready = true
	s.trackSent = -1
	var h struct {
		Baud int `json:"baud"`
	}
	json.Unmarshal(p, &h)
	if s.asked == 0 && h.Baud == link.LinkBaud && s.rate == link.LinkBaud {
		if r := s.nextRate(); r != s.rate {
			debugf("asking the panel for %d baud", r)
			s.asked, s.askedAt = r, time.Now()
			return false, s.send(link.Baud, binary.LittleEndian.AppendUint32(nil, uint32(r)))
		}
	}
	return s.asked == 0, nil
}

// onBaud handles the panel's answer to a rate request; it has switched
// when it says yes
func (s *session) onBaud(p []byte) error {
	want := s.asked
	s.asked = 0
	if want == 0 {
		return nil
	}
	got := 0
	if len(p) == 4 {
		got = int(binary.LittleEndian.Uint32(p))
	}
	if got != want {
		log.Printf("INFO the panel can't do %d baud", want)
		s.failed[want] = true
		return nil
	}
	if err := s.switchRate(want); err != nil {
		return err
	}
	s.confirming, s.switchedAt = true, time.Now()
	return nil
}

// checkRate gives up on a rate the panel can't be heard at, or can only
// be heard through errors; bad is the decoder's bad frame count
func (s *session) checkRate(now time.Time, bad int) error {
	if s.asked != 0 && now.Sub(s.askedAt) > baudReplyWait {
		debugf("no answer to the %d baud request", s.asked)
		s.asked = 0
	}
	if s.rate == link.LinkBaud {
		return nil
	}
	if now.Sub(s.badSince) > linkBadWindow {
		s.badBase, s.badSince = bad, now
	}
	switch {
	case s.confirming && now.Sub(s.switchedAt) > baudConfirmWait:
		log.Printf("WARNING nothing from the panel at %d baud; back to %d", s.rate, link.LinkBaud)
		s.failed[s.rate] = true
		s.confirming = false
	case !s.confirming && bad-s.badBase >= linkBadMax:
		log.Printf("WARNING %d bad frames from the panel at %d baud; back to %d", bad-s.badBase, s.rate, link.LinkBaud)
		s.failed[s.rate] = true
	case !s.confirming && now.Sub(s.lastFrame) > linkQuietWait:
		log.Printf("WARNING the panel went quiet at %d baud; back to %d", s.rate, link.LinkBaud)
	default:
		return nil
	}
	return s.switchRate(link.LinkBaud)
}

func (s *session) sendTrack() error {
	ver, data := s.st.trail.Payload()
	if int64(ver) == s.trackSent {
		return nil
	}
	s.trackSent = int64(ver)
	return s.send(link.Track, data)
}

func (s *session) consent(payload []byte) {
	if s.st.tiles == nil {
		return
	}
	var req struct {
		URL   *string `json:"url"`
		Allow *bool   `json:"allow"`
	}
	if err := json.Unmarshal(payload, &req); err != nil || req.URL == nil || req.Allow == nil {
		log.Printf("WARNING bad consent frame from panel")
		return
	}
	if err := s.st.tiles.SetConsent(*req.URL, *req.Allow); err != nil {
		log.Printf("INFO panel consent ignored: %v", err)
	}
}

func (s *session) tileRequest(payload []byte) {
	r, ok := link.DecodeTileRequest(payload)
	if !ok {
		log.Printf("WARNING bad tile request from panel")
		return
	}
	s.pendMu.Lock()
	busy := s.pending >= serialTileQueue
	if !busy {
		s.pending++
	}
	s.pendMu.Unlock()
	if busy {
		s.send(link.Tile, link.TileReply(r, link.TileFailed, nil))
		return
	}
	s.tileJobs <- r
}

func (s *session) tileWorker() {
	for r := range s.tileJobs {
		status, png := link.TileFailed, []byte(nil)
		if s.st.tiles == nil {
			status = link.TileUnavailable
		} else {
			skin := "classic"
			if int(r.Skin) < len(tiles.Skins) {
				skin = tiles.Skins[r.Skin]
			}
			var err error
			if r.Rot != 0 {
				png, err = s.st.tiles.GetTurned(skin, int(r.Z), int(r.X), int(r.Y), int(r.Rot), r.CachedOnly)
			} else {
				png, err = s.st.tiles.Get(skin, int(r.Z), int(r.X), int(r.Y), r.CachedOnly)
			}
			var te *tiles.Error
			switch {
			case err == nil:
				status = link.TileOK
			case errors.As(err, &te):
				switch te.Status {
				case 400, 404:
					status = link.TileNotFound
				case 403, 503:
					status = link.TileUnavailable
				}
				debugf("panel tile %d/%d/%d: %s", r.Z, r.X, r.Y, te.Reason)
			default:
				log.Printf("ERROR panel tile %d/%d/%d: %v", r.Z, r.X, r.Y, err)
			}
		}
		s.pendMu.Lock()
		s.pending--
		s.pendMu.Unlock()
		if err := s.send(link.Tile, link.TileReply(r, status, png)); err != nil {
			debugf("tile reply not sent: %v", err)
		}
	}
}

// field reads a view frame value; JSON numbers and booleans both work
func field(m map[string]any) func(string) (string, bool) {
	return func(k string) (string, bool) {
		switch v := m[k].(type) {
		case float64:
			if k == "lat" || k == "lon" {
				return fmt.Sprint(v), true
			}
			return fmt.Sprint(int64(v)), true
		case bool:
			if v {
				return "1", true
			}
			return "0", true
		}
		return "", false
	}
}

func (s *session) view(payload []byte) {
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		log.Printf("WARNING bad view frame from panel: %v", err)
		return
	}
	v, err := markers.ViewFrom(field(m))
	if err != nil {
		log.Printf("WARNING bad view frame from panel: %v", err)
		return
	}
	b := s.st.devmap.Markers(v)
	debugf("panel view z%d phy %#x new %v age %d: %d markers", v.Z, v.PhyMask, v.NewOnly, v.MaxAge,
		binary.LittleEndian.Uint16(b[4:]))
	s.send(link.Markers, b)
}

func (s *session) detail(payload []byte) {
	if len(payload) != 4 {
		return
	}
	var out any = map[string]int{"id": 0}
	if d, ok := s.st.devmap.Detail(binary.LittleEndian.Uint32(payload)); ok {
		out = d
	}
	b, _ := json.Marshal(out)
	s.send(link.Detail, b)
}

func (l *serialLink) session(f serialPort, setRate func(int) error) error {
	if l.failed == nil {
		l.failed = map[int]bool{}
	}
	s := &session{serialLink: l, f: f, setRate: setRate, rate: link.LinkBaud,
		tileJobs: make(chan link.TileRequest, serialTileQueue), trackSent: -1, lastFrame: time.Now()}
	defer close(s.tileJobs)
	for i := 0; i < serialTileWorkers; i++ {
		go s.tileWorker()
	}

	type chunk struct {
		b   []byte
		err error
	}
	reads := make(chan chunk)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			b := make([]byte, 4096)
			n, err := f.Read(b)
			select {
			case reads <- chunk{b[:n], err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	dec := link.NewDecoder(link.MaxPayload)
	line := ""
	// the first push waits for the panel's hello
	timer := time.NewTimer(l.interval)
	defer timer.Stop()
	sendNow := func() { timer.Reset(0) }
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			if s.ready && s.asked == 0 {
				if err := s.send(link.State, l.st.snapshot()); err != nil {
					return err
				}
				if err := s.sendTrack(); err != nil {
					return err
				}
			}
			timer.Reset(l.interval)
		case <-l.st.notes.changed:
			sendNow()
		case now := <-tick.C:
			if err := s.checkRate(now, dec.Bad); err != nil {
				return err
			}
		case c := <-reads:
			if c.err != nil {
				return c.err
			}
			if len(c.b) == 0 {
				return errors.New("serial port closed")
			}
			frames, text := dec.Feed(c.b)
			for _, fr := range frames {
				s.lastFrame = time.Now()
				if s.confirming {
					s.confirming = false
					s.badBase, s.badSince = dec.Bad, time.Now()
					log.Printf("INFO serial link at %d baud", s.rate)
				}
				switch fr.Type {
				case link.Hello:
					push, err := s.onHello(fr.Payload)
					if err != nil {
						return err
					}
					if push {
						sendNow()
					}
				case link.Baud:
					if err := s.onBaud(fr.Payload); err != nil {
						return err
					}
					if s.confirming {
						sendNow()
					}
				case link.Consent:
					s.consent(fr.Payload)
					sendNow()
				case link.TileReq:
					s.tileRequest(fr.Payload)
				case link.View:
					s.view(fr.Payload)
				case link.DetailReq:
					s.detail(fr.Payload)
				default:
					debugf("ignoring frame type %d from panel", fr.Type)
				}
			}
			line += strings.ToValidUTF8(string(text), "�")
			lines := strings.Split(line, "\n")
			line = lines[len(lines)-1]
			for _, ln := range lines[:len(lines)-1] {
				if t := unGarble(ln); t != "" {
					log.Printf("INFO panel: %s", t)
				}
			}
			if len(line) > 512 {
				line = line[len(line)-512:]
			}
		}
	}
}

// unGarble drops what was read at the wrong rate, around a rate change,
// from the front of a log line
func unGarble(s string) string {
	if i := strings.LastIndexFunc(s, func(r rune) bool { return r == '�' || r < ' ' && r != '\t' && r != '\r' }); i >= 0 {
		_, n := utf8.DecodeRuneInString(s[i:])
		s = s[i+n:]
	}
	return strings.TrimSpace(s)
}

// waitHello reads until the panel's hello or the deadline
func waitHello(f interface {
	io.Reader
	SetReadDeadline(time.Time) error
}, wait time.Duration) bool {
	defer f.SetReadDeadline(time.Time{})
	f.SetReadDeadline(time.Now().Add(wait))
	dec := link.NewDecoder(link.MaxPayload)
	b := make([]byte, 4096)
	for {
		n, err := f.Read(b)
		frames, _ := dec.Feed(b[:n])
		for _, fr := range frames {
			if fr.Type == link.Hello {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

func (l *serialLink) open() (string, *os.File, error) {
	if l.path != serialAuto {
		f, err := openSerial(l.path, link.LinkBaud)
		return l.path, f, err
	}
	if l.prober == nil {
		l.prober = &portProber{}
	}
	return l.prober.find(panelPorts(), probeWait)
}

func (l *serialLink) run() {
	logged := false
	for {
		path, f, err := l.open()
		if err != nil {
			if !logged {
				log.Printf("WARNING serial %s: %v; retrying", l.path, err)
				logged = true
			}
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("INFO serial %s open at %d baud", path, link.LinkBaud)
		logged = false
		err = l.session(f, func(rate int) error {
			if err := drainSerial(f); err != nil {
				return err
			}
			return setBaud(f, rate)
		})
		if err != nil {
			log.Printf("WARNING serial %s: %v", path, err)
		}
		f.Close()
		time.Sleep(time.Second)
	}
}
