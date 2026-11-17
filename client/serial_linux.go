package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var bauds = map[int]uint32{
	9600: syscall.B9600, 19200: syscall.B19200, 38400: syscall.B38400, 57600: syscall.B57600,
	115200: syscall.B115200, 230400: syscall.B230400, 460800: syscall.B460800, 500000: syscall.B500000,
	921600: syscall.B921600, 1000000: syscall.B1000000, 1500000: syscall.B1500000, 2000000: syscall.B2000000,
}

// TCFLSH, missing from package syscall
const tcflsh = 0x540b

func baudOK(baud int) bool { _, ok := bauds[baud]; return ok }

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// openSerial opens a port raw, 8N1, no hangup on close.  The fd is
// non-blocking, so reads and writes go through Go's poller and take
// deadlines.
func openSerial(path string, baud int) (*os.File, error) {
	if !baudOK(baud) {
		return nil, fmt.Errorf("unsupported baud rate %d", baud)
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := setBaud(f, baud); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
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
