package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// IOSSIOSPEED, _IOW('T', 2, speed_t): sets any baud rate on macOS serial
// drivers, including those above B230400 that termios can't express
const iossiospeed = 0x80085402

func baudOK(baud int) bool { return baud >= 300 && baud <= 4000000 }

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// openPort opens a port without changing its settings.  The fd is
// non-blocking, so reads and writes go through Go's poller and take
// deadlines.  Use the /dev/cu.* node, not /dev/tty.*, which waits for DCD.
func openPort(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
}

// openSerial opens a port raw, 8N1, no hangup on close, and exclusive, so
// nothing else opens it while we have it.
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

// macOS can't ask; opening a port another program holds exclusive fails
// with EBUSY instead
func portExclusive(*os.File) bool { return false }

func getTermios(f *os.File) (*syscall.Termios, error) {
	var t syscall.Termios
	return &t, fdIoctl(f, syscall.TIOCGETA, unsafe.Pointer(&t))
}

func setTermios(f *os.File, t *syscall.Termios) error {
	return fdIoctl(f, syscall.TIOCSETA, unsafe.Pointer(t))
}

// setBaud puts an open port in raw 8N1 at baud and flushes it.
func setBaud(f *os.File, baud int) error {
	if !baudOK(baud) {
		return fmt.Errorf("unsupported baud rate %d", baud)
	}
	rc, err := f.SyscallConn()
	if err == nil {
		cerr := rc.Control(func(fd uintptr) {
			var t syscall.Termios
			if err = ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(&t)); err != nil {
				return
			}
			speed := uint64(syscall.B9600)
			if baud <= 230400 {
				speed = uint64(baud)
			}
			t.Iflag, t.Oflag, t.Lflag = 0, 0, 0
			t.Cflag = syscall.CS8 | syscall.CREAD | syscall.CLOCAL
			t.Ispeed, t.Ospeed = speed, speed
			// VMIN 1: an idle read is EAGAIN for the poller, not a 0-byte EOF
			t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
			if err = ioctl(fd, syscall.TIOCSETA, unsafe.Pointer(&t)); err != nil {
				return
			}
			if baud > 230400 {
				s := uint64(baud)
				// a pty has no line speed and refuses this
				if err = ioctl(fd, iossiospeed, unsafe.Pointer(&s)); errors.Is(err, syscall.ENOTTY) {
					err = nil
				} else if err != nil {
					return
				}
			}
			which := int32(3) // FREAD | FWRITE
			err = ioctl(fd, syscall.TIOCFLUSH, unsafe.Pointer(&which))
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
		// a pty has nothing to drain
		if e := ioctl(fd, syscall.TIOCDRAIN, nil); e != nil && !errors.Is(e, syscall.ENOTTY) {
			err = e
		}
	})
	if err == nil {
		err = cerr
	}
	return err
}

// panelPorts lists the USB serial ports a CH340 shows up as: Apple's driver
// names it usbserial-*, WCH's wchusbserial*
func panelPorts() []string {
	var out []string
	for _, g := range []string{"/dev/cu.wchusbserial*", "/dev/cu.usbserial-*"} {
		m, _ := filepath.Glob(g)
		out = append(out, m...)
	}
	return out
}

// portInUse names a program that has the port open, from lsof, or is "".
// Without root lsof only sees this user's programs.
func portInUse(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "/usr/sbin/lsof", "-w", "-Fpc", "--", path).Output()
	pid, name := "", ""
	for _, ln := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(ln, "p"):
			pid, name = ln[1:], ""
		case strings.HasPrefix(ln, "c"):
			name = ln[1:]
		}
		if pid != "" && name != "" && pid != strconv.Itoa(os.Getpid()) {
			return name + "[" + pid + "]"
		}
	}
	return ""
}

func bridgeHint() string { return "" }
