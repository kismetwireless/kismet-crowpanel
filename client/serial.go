package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"kismet-crowpanel/link"
	"kismet-crowpanel/markers"
	"kismet-crowpanel/tiles"
)

const (
	serialTileWorkers = 4
	serialTileQueue   = 8
	serialWriteWait   = 2 * time.Second
)

// serialLink pushes the state document to the panel over a serial port,
// and answers its hello, view, tile, detail and consent frames.  Tiles are
// fetched on workers so a slow tile server doesn't hold up the state.
// Reopens the port if it goes away (unplug, reflash).
type serialLink struct {
	st       *state
	path     string
	baud     int
	interval time.Duration
}

// session is one open port
type session struct {
	*serialLink
	f         *os.File
	wmu       sync.Mutex
	tileJobs  chan link.TileRequest
	pendMu    sync.Mutex
	pending   int
	trackSent int64
}

func (s *session) send(t link.Type, payload []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.f.SetWriteDeadline(time.Now().Add(serialWriteWait))
	_, err := s.f.Write(link.Encode(t, payload))
	return err
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
			png, err = s.st.tiles.Get(skin, int(r.Z), int(r.X), int(r.Y), r.CachedOnly)
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

func (l *serialLink) session(f *os.File) error {
	s := &session{serialLink: l, f: f, tileJobs: make(chan link.TileRequest, serialTileQueue), trackSent: -1}
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
	timer := time.NewTimer(0)
	defer timer.Stop()
	sendNow := func() { timer.Reset(0) }
	for {
		select {
		case <-timer.C:
			if err := s.send(link.State, l.st.snapshot()); err != nil {
				return err
			}
			if err := s.sendTrack(); err != nil {
				return err
			}
			timer.Reset(l.interval)
		case c := <-reads:
			if c.err != nil {
				return c.err
			}
			if len(c.b) == 0 {
				return errors.New("serial port closed")
			}
			frames, text := dec.Feed(c.b)
			for _, fr := range frames {
				switch fr.Type {
				case link.Hello:
					log.Printf("INFO panel hello: %s", strings.ToValidUTF8(string(fr.Payload), "�"))
					s.trackSent = -1
					sendNow()
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
				if t := strings.TrimSpace(ln); t != "" {
					log.Printf("INFO panel: %s", t)
				}
			}
			if len(line) > 512 {
				line = line[len(line)-512:]
			}
		}
	}
}

func (l *serialLink) run() {
	logged := false
	for {
		f, err := openSerial(l.path, l.baud)
		if err != nil {
			if !logged {
				log.Printf("WARNING serial %s: %v; retrying", l.path, err)
				logged = true
			}
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("INFO serial %s open at %d baud", l.path, l.baud)
		logged = false
		if err := l.session(f); err != nil {
			log.Printf("WARNING serial %s: %v", l.path, err)
		}
		f.Close()
		time.Sleep(time.Second)
	}
}
