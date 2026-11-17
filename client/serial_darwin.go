package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
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

// openSerial opens a port raw, 8N1, no hangup on close.  The fd is
// non-blocking, so reads and writes go through Go's poller and take
// deadlines.  Use the /dev/cu.* node, not /dev/tty.*, which waits for DCD.
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
