//go:build !linux && !darwin

package main

import (
	"errors"
	"os"
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
