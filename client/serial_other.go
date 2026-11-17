//go:build !linux && !darwin

package main

import (
	"errors"
	"os"
	"time"
)

func baudOK(int) bool { return true }

func openSerial(string, int) (*os.File, error) {
	return nil, errors.New("serial ports are only supported on Linux and macOS")
}

func setBaud(*os.File, int) error {
	return errors.New("serial ports are only supported on Linux and macOS")
}

func setLines(*os.File, bool, bool) error {
	return errors.New("serial ports are only supported on Linux and macOS")
}

func drainSerial(*os.File) error {
	return errors.New("serial ports are only supported on Linux and macOS")
}

func panelPorts() []string { return nil }

func openPort(string) (*os.File, error) {
	return nil, errors.New("serial ports are only supported on Linux and macOS")
}

func portInUse(string) string { return "" }

type portProber struct{}

func (*portProber) find([]string, time.Duration) (string, *os.File, error) {
	return "", nil, errors.New("serial ports are only supported on Linux and macOS")
}
