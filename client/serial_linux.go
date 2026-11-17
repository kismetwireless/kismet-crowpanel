package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

var bauds = map[int]uint32{
	9600: syscall.B9600, 19200: syscall.B19200, 38400: syscall.B38400, 57600: syscall.B57600,
	115200: syscall.B115200, 230400: syscall.B230400, 460800: syscall.B460800, 500000: syscall.B500000,
	921600: syscall.B921600, 1000000: syscall.B1000000, 1500000: syscall.B1500000, 2000000: syscall.B2000000,
}

// TCFLSH, TCSBRK and TIOCGEXCL, missing from package syscall
const (
	tcflsh    = 0x540b
	tcsbrk    = 0x5409
	tiocgexcl = 0x80045440
)

func baudOK(baud int) bool { _, ok := bauds[baud]; return ok }

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// openPort opens a port without changing its settings.  The fd is
// non-blocking, so reads and writes go through Go's poller and take
// deadlines.
func openPort(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
}

// openSerial opens a port raw, 8N1, no hangup on close, and exclusive, so
// nothing else (ModemManager probing, say) opens it while we have it.
func openSerial(path string, baud int) (*os.File, error) {
	if !baudOK(baud) {
		return nil, fmt.Errorf("unsupported baud rate %d", baud)
	}
	f, err := openPort(path)
	if err != nil {
		return nil, err
	}
	if err := setBaud(f, baud); err != nil {
		f.Close()
		return nil, err
	}
	setExclusive(f)
	return f, nil
}

func fdIoctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	if cerr := rc.Control(func(fd uintptr) { err = ioctl(fd, req, arg) }); cerr != nil {
		return cerr
	}
	return err
}

func setExclusive(f *os.File) error { return fdIoctl(f, syscall.TIOCEXCL, nil) }

// portExclusive reports whether another program holds the port exclusive
// (gpsd does); root can open such a port anyway
func portExclusive(f *os.File) bool {
	var on int32
	return fdIoctl(f, tiocgexcl, unsafe.Pointer(&on)) == nil && on != 0
}

func getTermios(f *os.File) (*syscall.Termios, error) {
	var t syscall.Termios
	return &t, fdIoctl(f, syscall.TCGETS, unsafe.Pointer(&t))
}

func setTermios(f *os.File, t *syscall.Termios) error {
	return fdIoctl(f, syscall.TCSETS, unsafe.Pointer(t))
}

// setBaud puts an open port in raw 8N1 at baud and flushes it.
func setBaud(f *os.File, baud int) error {
	speed, ok := bauds[baud]
	if !ok {
		return fmt.Errorf("unsupported baud rate %d", baud)
	}
	rc, err := f.SyscallConn()
	if err == nil {
		cerr := rc.Control(func(fd uintptr) {
			var t syscall.Termios
			if err = ioctl(fd, syscall.TCGETS, unsafe.Pointer(&t)); err != nil {
				return
			}
			t.Iflag, t.Oflag, t.Lflag = 0, 0, 0
			t.Cflag = syscall.CS8 | syscall.CREAD | syscall.CLOCAL | speed
			t.Ispeed, t.Ospeed = speed, speed
			// VMIN 1: an idle read is EAGAIN for the poller, not a 0-byte EOF
			t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
			if err = ioctl(fd, syscall.TCSETS, unsafe.Pointer(&t)); err != nil {
				return
			}
			_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, tcflsh, syscall.TCIOFLUSH)
			if e != 0 {
				err = e
			}
		})
		if err == nil {
			err = cerr
		}
	}
	return err
}

// drainSerial waits until everything written to the port has gone out
func drainSerial(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	cerr := rc.Control(func(fd uintptr) {
		// TCSBRK with a nonzero argument is tcdrain
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, tcsbrk, 1); e != 0 {
			err = e
		}
	})
	if err == nil {
		err = cerr
	}
	return err
}

// USB vendor:product of the serial bridges on panels: the CH340, and the
// CH9102 some newer boards have
var panelBridges = []string{"1a86:7523", "1a86:55d4"}

// panelPorts lists the serial ports behind a panel's USB bridge, from sysfs,
// without opening anything
func panelPorts() []string {
	var out []string
	ttys, _ := filepath.Glob("/sys/class/tty/*")
	for _, t := range ttys {
		if id := usbID(filepath.Join(t, "device")); slices.Contains(panelBridges, id) {
			out = append(out, "/dev/"+filepath.Base(t))
		}
	}
	return out
}

// usbID finds vendor:product of the USB device above a tty's device
func usbID(dev string) string {
	p, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return ""
	}
	for i := 0; i < 4 && p != "/"; i++ {
		v, err1 := os.ReadFile(filepath.Join(p, "idVendor"))
		d, err2 := os.ReadFile(filepath.Join(p, "idProduct"))
		if err1 == nil && err2 == nil {
			return strings.TrimSpace(string(v)) + ":" + strings.TrimSpace(string(d))
		}
		p = filepath.Dir(p)
	}
	return ""
}

// portInUse names a program that has the port open or locked, or is "".
// It sees UUCP lock files and flocks of any program, but open files only of
// programs it may inspect (its own user's, or all as root).
func portInUse(path string) string {
	var st syscall.Stat_t
	if syscall.Stat(path, &st) != nil {
		return ""
	}
	for _, dir := range []string{"/run/lock", "/var/lock"} {
		b, err := os.ReadFile(filepath.Join(dir, "LCK.."+filepath.Base(path)))
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if len(b) == 4 && pid == 0 {
			pid = int(binary.NativeEndian.Uint32(b)) // an old binary lock file
		}
		if pid > 0 && syscall.Kill(pid, 0) != syscall.ESRCH {
			return procName(pid) + " (lock file)"
		}
	}
	// /proc/locks: "1: FLOCK  ADVISORY  WRITE 1234 00:05:567 0 EOF"
	if b, err := os.ReadFile("/proc/locks"); err == nil {
		want := fmt.Sprintf("%02x:%02x:%d", devMajor(uint64(st.Dev)), devMinor(uint64(st.Dev)), uint64(st.Ino))
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Fields(ln)
			if i := slices.Index(f, want); i > 0 {
				if pid, err := strconv.Atoi(f[i-1]); err == nil {
					return procName(pid) + " (lock)"
				}
			}
		}
	}
	self := os.Getpid()
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range fds {
		var o syscall.Stat_t
		if syscall.Stat(fd, &o) != nil || o.Mode&syscall.S_IFMT != syscall.S_IFCHR || uint64(o.Rdev) != uint64(st.Rdev) {
			continue
		}
		pid, _ := strconv.Atoi(strings.Split(fd, "/")[2])
		if pid != self {
			return procName(pid)
		}
	}
	return ""
}

func procName(pid int) string {
	comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if c := strings.TrimSpace(string(comm)); c != "" {
		return fmt.Sprintf("%s[%d]", c, pid)
	}
	return fmt.Sprintf("pid %d", pid)
}

// the kernel's dev_t split, as /proc/locks prints it
func devMajor(dev uint64) uint64 { return (dev >> 8) & 0xfff }
func devMinor(dev uint64) uint64 { return (dev & 0xff) | ((dev >> 12) & 0xfff00) }

// bridgeHint explains a panel's USB bridge with no serial port, which on
// Ubuntu is usually brltty claiming the CH340
func bridgeHint() string {
	devs, _ := filepath.Glob("/sys/bus/usb/devices/*")
	for _, d := range devs {
		if !slices.Contains(panelBridges, usbID(d)) {
			continue
		}
		if ttys, _ := filepath.Glob(filepath.Join(d, "*", "tty*")); len(ttys) == 0 {
			return fmt.Sprintf("USB device %s (%s) has no serial port; is brltty holding it? "+
				"(remove brltty, or mask its udev rule)", filepath.Base(d), usbID(d))
		}
	}
	return ""
}
