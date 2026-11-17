package main

import (
	"encoding/binary"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"kismet-crowpanel/link"
)

// fakePanel is the panel end of a pipe to a session
type fakePanel struct {
	t      *testing.T
	conn   net.Conn
	frames chan link.Frame
	mu     sync.Mutex
	rates  []int
	done   chan error
}

func startSession(t *testing.T, baud int) (*fakePanel, *serialLink) {
	host, panel := net.Pipe()
	l := &serialLink{st: newState("test"), baud: baud, interval: time.Hour}
	p := &fakePanel{t: t, conn: panel, frames: make(chan link.Frame, 32), done: make(chan error, 1)}
	go func() {
		p.done <- l.session(host, func(r int) error {
			p.mu.Lock()
			p.rates = append(p.rates, r)
			p.mu.Unlock()
			return nil
		})
	}()
	go func() {
		dec := link.NewDecoder(link.MaxPayload)
		b := make([]byte, 4096)
		for {
			n, err := panel.Read(b)
			fs, _ := dec.Feed(b[:n])
			for _, f := range fs {
				p.frames <- f
			}
			if err != nil {
				return
			}
		}
	}()
	return p, l
}

func (p *fakePanel) send(typ link.Type, payload []byte) {
	if _, err := p.conn.Write(link.Encode(typ, payload)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *fakePanel) hello(baud int) {
	p.send(link.Hello, []byte(`{"v":1,"board":"crowpanel7","baud":`+strconv.Itoa(baud)+`}`))
}

// next waits for a frame of type typ, skipping others
func (p *fakePanel) next(typ link.Type) link.Frame {
	p.t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case f := <-p.frames:
			if f.Type == typ {
				return f
			}
		case <-deadline:
			p.t.Fatalf("no frame of type %d", typ)
		}
	}
}

func (p *fakePanel) askedRate() int {
	p.t.Helper()
	return int(binary.LittleEndian.Uint32(p.next(link.Baud).Payload))
}

func (p *fakePanel) close() {
	p.conn.Close()
	<-p.done
}

func (p *fakePanel) switched() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.rates)
}

func TestSerialRateAccepted(t *testing.T) {
	p, l := startSession(t, 921600)
	p.hello(link.LinkBaud)
	if r := p.askedRate(); r != 921600 {
		t.Fatalf("asked for %d", r)
	}
	p.send(link.Baud, le32(921600))
	p.next(link.State)
	p.hello(921600)
	p.next(link.State)
	p.close()
	if r := p.switched(); !slices.Equal(r, []int{921600}) || len(l.failed) != 0 {
		t.Errorf("switched %v, failed %v", r, l.failed)
	}
}

func TestSerialRateRefused(t *testing.T) {
	p, l := startSession(t, 921600)
	p.hello(link.LinkBaud)
	p.askedRate()
	p.send(link.Baud, le32(0))
	p.hello(link.LinkBaud)
	if r := p.askedRate(); r != 460800 {
		t.Errorf("after a refusal asked for %d, want 460800", r)
	}
	p.close()
	if !l.failed[921600] || len(p.switched()) != 0 {
		t.Errorf("failed %v, switched %v", l.failed, p.switched())
	}
}

func TestSerialRateSilent(t *testing.T) {
	old := baudConfirmWait
	baudConfirmWait = 300 * time.Millisecond
	defer func() { baudConfirmWait = old }()

	p, l := startSession(t, 921600)
	p.hello(link.LinkBaud)
	p.askedRate()
	p.send(link.Baud, le32(921600))
	deadline := time.Now().Add(3 * time.Second)
	for len(p.switched()) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	p.close()
	if r := p.switched(); !slices.Equal(r, []int{921600, link.LinkBaud}) || !l.failed[921600] {
		t.Errorf("switched %v, failed %v", r, l.failed)
	}
}

func TestSerialOldFirmwareStays(t *testing.T) {
	p, _ := startSession(t, 921600)
	p.send(link.Hello, []byte(`{"v":1,"fw":"0.5"}`))
	p.next(link.State)
	p.close()
	if r := p.switched(); len(r) != 0 {
		t.Errorf("switched %v for firmware that can't", r)
	}
}

func TestNextRate(t *testing.T) {
	l := &serialLink{baud: 921600, failed: map[int]bool{}}
	for _, want := range []int{921600, 460800, 230400, link.LinkBaud} {
		got := l.nextRate()
		if got != want {
			t.Fatalf("next rate %d, want %d", got, want)
		}
		l.failed[got] = true
	}
	if (&serialLink{baud: link.LinkBaud}).nextRate() != link.LinkBaud {
		t.Error("--baud 115200 stays there")
	}
}

func TestSerialRateBadFrames(t *testing.T) {
	p, l := startSession(t, 921600)
	p.hello(link.LinkBaud)
	p.askedRate()
	p.send(link.Baud, le32(921600))
	p.hello(921600)
	p.next(link.State)
	bad := link.Encode(link.Hello, []byte(`{"v":1}`))
	bad[len(bad)-1] ^= 0xff
	for range linkBadMax {
		if _, err := p.conn.Write(bad); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(p.switched()) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	p.close()
	if r := p.switched(); !slices.Equal(r, []int{921600, link.LinkBaud}) || !l.failed[921600] {
		t.Errorf("switched %v, failed %v", r, l.failed)
	}
}

func TestParseBaud(t *testing.T) {
	if b, err := parseBaud("auto"); err != nil || b != linkRates[0] {
		t.Errorf("auto: %d, %v", b, err)
	}
	if b, err := parseBaud("460800"); err != nil || b != 460800 {
		t.Errorf("460800: %d, %v", b, err)
	}
	for _, v := range []string{"fast", "9600", ""} {
		if _, err := parseBaud(v); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
}
