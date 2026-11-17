//go:build linux || darwin

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// setLines sets the DTR and RTS modem lines, which the boards' auto-reset
// circuit wires to the ESP32's IO0 and EN.
func setLines(f *os.File, dtr, rts bool) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	cerr := rc.Control(func(fd uintptr) {
		set, clear := int32(0), int32(0)
		for _, l := range []struct {
			on  bool
			bit int32
		}{{dtr, syscall.TIOCM_DTR}, {rts, syscall.TIOCM_RTS}} {
			if l.on {
				set |= l.bit
			} else {
				clear |= l.bit
			}
		}
		if set != 0 {
			if err = ioctl(fd, syscall.TIOCMBIS, unsafe.Pointer(&set)); err != nil {
				return
			}
		}
		if clear != 0 {
			err = ioctl(fd, syscall.TIOCMBIC, unsafe.Pointer(&clear))
		}
	})
	if err == nil {
		err = cerr
	}
	return err
}
