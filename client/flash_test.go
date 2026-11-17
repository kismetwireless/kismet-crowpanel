package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kismet-crowpanel/link"
)

// fakeROM answers like the ESP32-S3 ROM loader, writing into a flash image
type fakeROM struct {
	conn      net.Conn
	flash     []byte
	comp      bytes.Buffer
	addr      uint32
	seq       uint32
	badBlock  int // fail the checksum of this block, -1 for none
	baud      uint32
	syncs     int
	blocksGot int
}

func (f *fakeROM) reply(op byte, val uint32, data []byte, status byte) {
	body := append(append([]byte{}, data...), status, 0x05, 0, 0)
	if status == 0 {
		body[len(data)+1] = 0
	}
	resp := make([]byte, 8)
	resp[0], resp[1] = 1, op
	binary.LittleEndian.PutUint16(resp[2:], uint16(len(body)))
	binary.LittleEndian.PutUint32(resp[4:], val)
	f.conn.Write(slipEncode(append(resp, body...)))
}

func (f *fakeROM) run() {
	// boot chatter before the loader, as on the real thing
	f.conn.Write([]byte("ESP-ROM:esp32s3-20210327\r\nwaiting for download\r\n"))
	r := bufio.NewReader(f.conn)
	for {
		req, err := readFrame(r)
		if err != nil {
			return
		}
		op := req[1]
		chk := binary.LittleEndian.Uint32(req[4:])
		data := req[8:]
		u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(data[4*i:]) }
		switch op {
		case opSync:
			f.syncs++
			if f.syncs < 2 {
				continue // the first sync goes unanswered, as it often does
			}
			for i := 0; i < 3; i++ {
				f.reply(op, 0, nil, 0)
			}
		case opReadReg:
			if u32(0) == chipMagicReg {
				f.inflate()
			}
			f.reply(op, esp32s3Magic, nil, 0)
		case opChangeBaud:
			f.baud = u32(0)
			f.reply(op, 0, nil, 0)
		case opSPIAttach, opSPISetParams:
			f.reply(op, 0, nil, 0)
		case opFlashDeflBeg:
			f.addr, f.seq = u32(3), 0
			f.comp.Reset()
			f.reply(op, 0, nil, 0)
		case opFlashDeflData:
			blk := data[16:]
			if checksum(blk) != chk || u32(1) != f.seq || f.blocksGot == f.badBlock {
				f.reply(op, 0, nil, 1)
				continue
			}
			f.blocksGot++
			f.seq++
			f.comp.Write(blk)
			f.reply(op, 0, nil, 0)
		case opFlashMD5:
			f.inflate()
			sum := md5.Sum(f.flash[u32(0) : u32(0)+u32(1)])
			f.reply(op, 0, []byte(hex.EncodeToString(sum[:])), 0)
		default:
			f.reply(op, 0, nil, 1)
		}
	}
}

func (f *fakeROM) inflate() {
	if f.comp.Len() == 0 {
		return
	}
	zr, err := zlib.NewReader(bytes.NewReader(f.comp.Bytes()))
	if err != nil {
		return
	}
	out, _ := io.ReadAll(zr)
	copy(f.flash[f.addr:], out)
}

func flashWith(t *testing.T, image []byte, addr uint32, badBlock int) (*fakeROM, []bool, error) {
	t.Helper()
	host, dev := net.Pipe()
	rom := &fakeROM{conn: dev, flash: bytes.Repeat([]byte{0xff}, flashSize), badBlock: badBlock}
	go rom.run()
	defer host.Close()
	defer dev.Close()
	l := newLoader(host)
	l.sleep = func(time.Duration) {}
	var lines []bool
	l.setLines = func(dtr, rts bool) error { lines = append(lines, dtr, rts); return nil }
	err := l.flash(image, addr, 460800, func(int, int) {})
	return rom, lines, err
}

func testImage(n int) []byte {
	img := make([]byte, n)
	img[0] = espImageMagic
	// pseudo-random half the way, so it compresses to many blocks
	x := uint32(1)
	for i := 1; i < n; i++ {
		x = x*1664525 + 1013904223
		img[i] = byte(x >> 24)
		if i > n/2 {
			img[i] = byte(i / 512)
		}
	}
	return img
}

func TestFlashWritesAndVerifies(t *testing.T) {
	img := testImage(300_003) // not a multiple of 4, several blocks compressed
	rom, lines, err := flashWith(t, img, 0x10000, -1)
	if err != nil {
		t.Fatalf("flash: %v", err)
	}
	if !bytes.Equal(rom.flash[0x10000:0x10000+len(img)], img) {
		t.Fatal("flash contents differ from the image")
	}
	if pad := rom.flash[0x10000+len(img) : 0x10000+len(img)+1]; pad[0] != 0xff {
		t.Fatalf("padding byte 0x%02x", pad[0])
	}
	if rom.baud != 460800 {
		t.Fatalf("baud change to %d", rom.baud)
	}
	// reset into the loader (EN low, then IO0 low), ending in a hard reset
	if len(lines) < 8 || !lines[1] || lines[0] || !lines[2] || lines[3] || !lines[len(lines)-3] {
		t.Fatalf("modem line sequence %v", lines)
	}
}

func TestFlashBadBlockFails(t *testing.T) {
	_, _, err := flashWith(t, testImage(100_000), 0, 2)
	if err == nil || !strings.Contains(err.Error(), "block 3") {
		t.Fatalf("want a block 3 error, got %v", err)
	}
}

func TestSlipRoundTrip(t *testing.T) {
	in := []byte{1, 0xc0, 2, 0xdb, 0xdb, 0xc0, 3}
	out, err := readFrame(bufio.NewReader(bytes.NewReader(append([]byte("junk\xc0"), slipEncode(in)...))))
	if err != nil || !bytes.Equal(out, in) {
		t.Fatalf("got %v %v", out, err)
	}
}

// detectWith runs detection against a panel that answers out at the given
// link speed after each reset
func detectWith(t *testing.T, answerAt int, out []byte) (string, []int, error) {
	t.Helper()
	host, dev := net.Pipe()
	defer host.Close()
	defer dev.Close()
	go io.Copy(io.Discard, dev)
	l := newLoader(host)
	l.sleep = func(time.Duration) {}
	baud := 0
	var tried []int
	l.setBaud = func(b int) error { baud = b; tried = append(tried, b); return nil }
	l.setLines = func(dtr, rts bool) error {
		// released from reset: the panel boots
		if !rts && baud == answerAt {
			go dev.Write(out)
		}
		return nil
	}
	b, err := l.detectBoard([]int{921600, 460800}, 300*time.Millisecond, func(string, ...any) {})
	return b, tried, err
}

func TestDetectBoard(t *testing.T) {
	cases := []struct {
		name string
		at   int
		out  []byte
		want string
	}{
		{"hello", 921600, append([]byte("noise\r\n"), link.Encode(link.Hello, []byte(`{"v":1,"board":"crowpanel5"}`))...), "crowpanel5"},
		{"boot line", 460800, []byte("ESP-ROM:esp32s3\r\nboard: crowpanel5\r\n"), "crowpanel5"},
		{"old hello", 460800, link.Encode(link.Hello, []byte(`{"v":1,"fw":"0.5"}`)), "crowpanel7"},
	}
	for _, c := range cases {
		got, tried, err := detectWith(t, c.at, c.out)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v", c.name, got, err)
		}
		if c.at == 460800 && len(tried) != 2 {
			t.Errorf("%s: tried %v", c.name, tried)
		}
	}
	if _, _, err := detectWith(t, 0, nil); err == nil {
		t.Error("silence: want an error")
	}
}

func TestFindImageOrder(t *testing.T) {
	installed, tree := t.TempDir(), t.TempDir()
	for _, d := range []string{installed, tree} {
		if err := os.WriteFile(filepath.Join(d, "crowpanel5.bin"), []byte{espImageMagic}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := findImage("crowpanel5", []string{installed, tree}); err != nil || filepath.Dir(p) != installed {
		t.Fatalf("got %q %v, want the installed one", p, err)
	}
	if _, err := findImage("crowpanel7", []string{installed, tree}); err == nil || !strings.Contains(err.Error(), installed) {
		t.Fatalf("want an error naming the dirs searched, got %v", err)
	}

	saved := dataDir
	defer func() { dataDir = saved }()
	dataDir = "/usr/share/kismet-crowpanel"
	if d := firmwareDirs(); d[0] != "/usr/share/kismet-crowpanel/firmware" {
		t.Fatalf("firmware dirs %v: want the configured data dir first", d)
	}
}
