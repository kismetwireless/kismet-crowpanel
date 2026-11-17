package main

// Flashing a panel over its USB serial port, through the ESP32-S3's ROM
// serial loader (the protocol esptool speaks), so the prebuilt images in
// firmware/prebuilt need nothing but this binary:
//
//	kismet-crowpanel flash --port /dev/ttyUSB0
//
// The 7.0" and 5.0" boards are the same module and look alike to the ROM
// loader, so the board comes from the firmware already running: its boot
// line and hello name it (a hello without one is from firmware before 5.0"
// support, so a 7.0").  Other firmware needs --board.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"kismet-crowpanel/link"
)

// ROM loader commands
const (
	opFlashBegin    = 0x02
	opSync          = 0x08
	opReadReg       = 0x0a
	opSPISetParams  = 0x0b
	opSPIAttach     = 0x0d
	opChangeBaud    = 0x0f
	opFlashDeflBeg  = 0x10
	opFlashDeflData = 0x11
	opFlashMD5      = 0x13
)

const (
	romBaud        = 115200
	flashBlock     = 0x400 // the ROM loader's write block
	flashSize      = 4 << 20
	chipMagicReg   = 0x40001000
	esp32s3Magic   = 0x9
	espImageMagic  = 0xe9
	checksumSeed   = 0xef
	defaultTimeout = 3 * time.Second
)

var flashBoards = []string{"crowpanel7", "crowpanel5"}

// link speeds to look for running firmware at: the default, then the one
// macOS needs
var detectBauds = []int{921600, 460800}

var bootBoardLine = regexp.MustCompile(`board: (\w+)`)

// flashPort is the serial port, or a pipe in tests
type flashPort interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
}

type loader struct {
	port     flashPort
	r        *bufio.Reader
	setBaud  func(int) error
	setLines func(dtr, rts bool) error
	sleep    func(time.Duration)
}

func newLoader(p flashPort) *loader {
	return &loader{port: p, r: bufio.NewReader(p), sleep: time.Sleep,
		setBaud: func(int) error { return nil }, setLines: func(bool, bool) error { return nil }}
}

func slipEncode(b []byte) []byte {
	out := make([]byte, 0, len(b)+8)
	out = append(out, 0xc0)
	for _, c := range b {
		switch c {
		case 0xc0:
			out = append(out, 0xdb, 0xdc)
		case 0xdb:
			out = append(out, 0xdb, 0xdd)
		default:
			out = append(out, c)
		}
	}
	return append(out, 0xc0)
}

// readFrame returns the next SLIP frame, skipping anything outside one (the
// ROM's boot messages)
func readFrame(r *bufio.Reader) ([]byte, error) {
	for {
		c, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if c == 0xc0 {
			break
		}
	}
	var f []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		switch {
		case c == 0xc0 && len(f) == 0:
			// back-to-back delimiters
		case c == 0xc0:
			return f, nil
		case c == 0xdb:
			e, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			switch e {
			case 0xdc:
				f = append(f, 0xc0)
			case 0xdd:
				f = append(f, 0xdb)
			default:
				return nil, fmt.Errorf("bad SLIP escape 0x%02x", e)
			}
		default:
			f = append(f, c)
		}
	}
}

func checksum(b []byte) uint32 {
	c := byte(checksumSeed)
	for _, x := range b {
		c ^= x
	}
	return uint32(c)
}

func le32(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}

// command sends one request and waits for its response: the value field and
// the data before the status bytes
func (l *loader) command(op byte, data []byte, chk uint32, timeout time.Duration) (uint32, []byte, error) {
	req := make([]byte, 8, 8+len(data))
	req[1] = op
	binary.LittleEndian.PutUint16(req[2:], uint16(len(data)))
	binary.LittleEndian.PutUint32(req[4:], chk)
	req = append(req, data...)
	if _, err := l.port.Write(slipEncode(req)); err != nil {
		return 0, nil, err
	}
	l.port.SetReadDeadline(time.Now().Add(timeout))
	defer l.port.SetReadDeadline(time.Time{})
	for {
		f, err := readFrame(l.r)
		if err != nil {
			return 0, nil, err
		}
		// responses to earlier syncs and the like turn up too
		if len(f) < 8 || f[0] != 1 || f[1] != op {
			continue
		}
		val := binary.LittleEndian.Uint32(f[4:])
		body := f[8:]
		if n := int(binary.LittleEndian.Uint16(f[2:])); n <= len(body) {
			body = body[:n]
		}
		if len(body) < 4 {
			return 0, nil, fmt.Errorf("command 0x%02x: short response", op)
		}
		st := body[len(body)-4:]
		if st[0] != 0 {
			return 0, nil, fmt.Errorf("command 0x%02x failed: error 0x%02x", op, st[1])
		}
		return val, body[:len(body)-4], nil
	}
}

// drain discards whatever arrives within d
func (l *loader) drain(d time.Duration) {
	l.port.SetReadDeadline(time.Now().Add(d))
	defer l.port.SetReadDeadline(time.Time{})
	io.Copy(io.Discard, l.r)
	l.r.Reset(l.port)
}

// resetToLoader pulses EN low with IO0 held low, through the boards'
// two-transistor auto-reset circuit (DTR drives IO0, RTS drives EN)
func (l *loader) resetToLoader() error {
	steps := []struct {
		dtr, rts bool
		wait     time.Duration
	}{{false, true, 100 * time.Millisecond}, {true, false, 50 * time.Millisecond}, {false, false, 0}}
	for _, s := range steps {
		if err := l.setLines(s.dtr, s.rts); err != nil {
			return err
		}
		l.sleep(s.wait)
	}
	return nil
}

func (l *loader) hardReset() error {
	if err := l.setLines(false, true); err != nil {
		return err
	}
	l.sleep(100 * time.Millisecond)
	return l.setLines(false, false)
}

// detectBoard restarts the panel and listens at each link speed for the
// running firmware to name its board
func (l *loader) detectBoard(bauds []int, wait time.Duration, logf func(string, ...any)) (string, error) {
	defer l.port.SetReadDeadline(time.Time{})
	for _, b := range bauds {
		logf("looking for the panel's firmware at %d baud", b)
		if err := l.setBaud(b); err != nil {
			return "", err
		}
		l.r.Reset(l.port)
		if err := l.hardReset(); err != nil {
			return "", err
		}
		l.port.SetReadDeadline(time.Now().Add(wait))
		dec := link.NewDecoder(link.MaxPayload)
		text := ""
		buf := make([]byte, 4096)
		for {
			n, err := l.r.Read(buf)
			frames, t := dec.Feed(buf[:n])
			for _, f := range frames {
				if f.Type != link.Hello {
					continue
				}
				var h struct {
					Board string `json:"board"`
				}
				if json.Unmarshal(f.Payload, &h) != nil {
					continue
				}
				if h.Board == "" {
					return "crowpanel7", nil
				}
				if slices.Contains(flashBoards, h.Board) {
					return h.Board, nil
				}
				return "", fmt.Errorf("the panel's firmware says it's a %q, which has no image here", h.Board)
			}
			text += string(t)
			if m := bootBoardLine.FindStringSubmatch(text); m != nil && slices.Contains(flashBoards, m[1]) {
				return m[1], nil
			}
			if i := strings.LastIndexByte(text, '\n'); i >= 0 {
				text = text[i+1:]
			}
			if err != nil {
				break
			}
		}
	}
	return "", errors.New("no kismet-crowpanel firmware answered, so the board is unknown")
}

func (l *loader) connect() error {
	if err := l.setBaud(romBaud); err != nil {
		return err
	}
	sync := append([]byte{0x07, 0x07, 0x12, 0x20}, bytes.Repeat([]byte{0x55}, 32)...)
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if err = l.resetToLoader(); err != nil {
			return err
		}
		l.drain(200 * time.Millisecond)
		for i := 0; i < 5; i++ {
			if _, _, err = l.command(opSync, sync, 0, 100*time.Millisecond); err == nil {
				// the ROM answers a sync several times over
				l.drain(100 * time.Millisecond)
				return nil
			}
		}
	}
	return fmt.Errorf("no answer from the ROM loader (%v); hold BOOT while pressing RESET and try again", err)
}

func timeoutPerMB(perMB time.Duration, n int) time.Duration {
	t := time.Duration(float64(perMB) * float64(n) / float64(1<<20))
	return max(t, defaultTimeout)
}

// flash writes image at addr, compressed, and checks it by MD5
func (l *loader) flash(image []byte, addr uint32, baud int, progress func(done, total int)) error {
	if err := l.connect(); err != nil {
		return err
	}
	magic, _, err := l.command(opReadReg, le32(chipMagicReg), 0, defaultTimeout)
	if err != nil {
		return err
	}
	if magic != esp32s3Magic {
		return fmt.Errorf("not an ESP32-S3 (chip magic 0x%x)", magic)
	}
	if baud != romBaud {
		if _, _, err := l.command(opChangeBaud, le32(uint32(baud), 0), 0, defaultTimeout); err != nil {
			return err
		}
		if err := l.setBaud(baud); err != nil {
			return err
		}
		l.sleep(50 * time.Millisecond)
		l.drain(50 * time.Millisecond)
	}
	if _, _, err := l.command(opSPIAttach, le32(0, 0), 0, defaultTimeout); err != nil {
		return err
	}
	if _, _, err := l.command(opSPISetParams, le32(0, flashSize, 64<<10, 4<<10, 256, 0xffff), 0, defaultTimeout); err != nil {
		return err
	}

	for len(image)%4 != 0 {
		image = append(image, 0xff)
	}
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestCompression)
	zw.Write(image)
	zw.Close()
	comp := z.Bytes()

	blocks := (len(comp) + flashBlock - 1) / flashBlock
	erase := (len(image) + flashBlock - 1) / flashBlock * flashBlock
	if _, _, err := l.command(opFlashDeflBeg, le32(uint32(erase), uint32(blocks), flashBlock, addr, 0), 0,
		timeoutPerMB(30*time.Second, erase)); err != nil {
		return fmt.Errorf("erase: %w", err)
	}
	for seq := 0; seq < blocks; seq++ {
		blk := comp[seq*flashBlock : min((seq+1)*flashBlock, len(comp))]
		data := append(le32(uint32(len(blk)), uint32(seq), 0, 0), blk...)
		if _, _, err := l.command(opFlashDeflData, data, checksum(blk), 10*time.Second); err != nil {
			return fmt.Errorf("block %d of %d: %w", seq+1, blocks, err)
		}
		progress(seq+1, blocks)
	}
	// the last block is written once this answers
	if _, _, err := l.command(opReadReg, le32(chipMagicReg), 0, defaultTimeout); err != nil {
		return err
	}

	_, body, err := l.command(opFlashMD5, le32(addr, uint32(len(image)), 0, 0), 0, timeoutPerMB(8*time.Second, len(image)))
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	sum := md5.Sum(image)
	got := string(body)
	if len(body) == 16 {
		got = hex.EncodeToString(body)
	}
	if want := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("verify: flash MD5 %s, image %s", got, want)
	}
	return l.hardReset()
}

// findImage looks for <board>.bin in firmware/prebuilt beside the binary
// (client/), then under the current directory
func findImage(board string) (string, error) {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "..", "firmware", "prebuilt"))
	}
	dirs = append(dirs, filepath.Join("firmware", "prebuilt"))
	for _, d := range dirs {
		p := filepath.Join(d, board+".bin")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no %s.bin in firmware/prebuilt; give its path with --image", board)
}

func flashMain(args []string) int {
	fs := flag.NewFlagSet("flash", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: kismet-crowpanel flash --port PORT [--board BOARD | --image FILE] [options]\n\n"+
			"Flashes a panel's firmware over its USB serial port, no esptool or PlatformIO needed.\n"+
			"Without --board or --image it finds the board from the firmware already on the panel\n"+
			"and flashes that board's prebuilt image.\n\n")
		fs.PrintDefaults()
	}
	port := fs.String("port", "", "the panel's serial `port` (/dev/ttyUSB0, /dev/cu.usbserial-*)")
	board := fs.String("board", "", "board, for its prebuilt image: crowpanel7 (7.0\") or crowpanel5 (5.0\");\n"+
		"found from the firmware already on the panel when not given")
	linkBaud := fs.Int("link-baud", 0, "the running firmware's link speed, for finding the board (default: try 921600, then 460800)")
	imagePath := fs.String("image", "", "flash this image `file` instead of the board's prebuilt one")
	addr := fs.Uint("address", 0, "flash address (0 for a merged image, 0x10000 for an app alone)")
	baud := fs.Int("baud", 460800, "baud rate while flashing (the ROM starts at 115200)")
	dryRun := fs.Bool("dry-run", false, "find the board and image, but don't flash")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	fail := func(f string, a ...any) int {
		fmt.Fprintf(os.Stderr, "flash: "+f+"\n", a...)
		return 1
	}
	if *port == "" || fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	if *board != "" && !slices.Contains(flashBoards, *board) {
		return fail("unknown board %q (crowpanel7 or crowpanel5)", *board)
	}
	bauds := detectBauds
	if *linkBaud != 0 {
		bauds = []int{*linkBaud}
	}
	for _, b := range append([]int{*baud}, bauds...) {
		if !baudOK(b) {
			return fail("unsupported baud rate %d", b)
		}
	}

	f, err := openSerial(*port, romBaud)
	if err != nil {
		return fail("%s: %v", *port, err)
	}
	defer f.Close()
	l := newLoader(f)
	l.setBaud = func(b int) error { return setBaud(f, b) }
	l.setLines = func(dtr, rts bool) error { return setLines(f, dtr, rts) }
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "flash: "+format+"\n", a...) }

	path := *imagePath
	if path == "" {
		if *board == "" {
			b, err := l.detectBoard(bauds, 7*time.Second, logf)
			if err != nil {
				return fail("%v; give --board crowpanel7 or --board crowpanel5", err)
			}
			logf("found a %s", b)
			*board = b
		}
		p, err := findImage(*board)
		if err != nil {
			return fail("%v", err)
		}
		path = p
	}
	image, err := os.ReadFile(path)
	if err != nil {
		return fail("%v", err)
	}
	if len(image) == 0 || uint64(*addr)+uint64(len(image)) > flashSize {
		return fail("%s doesn't fit in %d MB of flash at 0x%x", path, flashSize>>20, *addr)
	}
	if image[0] != espImageMagic {
		return fail("%s isn't an ESP32 image", path)
	}

	if *dryRun {
		logf("would write %s (%d KB) to %s at 0x%x", path, len(image)>>10, *port, *addr)
		return 0
	}
	fmt.Fprintf(os.Stderr, "flash: writing %s (%d KB) to %s at 0x%x\n", path, len(image)>>10, *port, *addr)
	start := time.Now()
	last := -1
	err = l.flash(image, uint32(*addr), *baud, func(done, total int) {
		if pct := done * 100 / total; pct/10 != last/10 {
			last = pct
			fmt.Fprintf(os.Stderr, "flash: %d%%\n", pct)
		}
	})
	if err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(os.Stderr, "flash: done and verified in %.1f s; the panel is restarting\n", time.Since(start).Seconds())
	return 0
}
