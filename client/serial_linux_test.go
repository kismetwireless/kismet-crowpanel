package main

import "testing"

func TestDevSplit(t *testing.T) {
	// makedev(259, 3), as /proc/locks prints it: 103:03
	dev := uint64(259)<<8 | 3
	if devMajor(dev) != 259 || devMinor(dev) != 3 {
		t.Errorf("major %d minor %d", devMajor(dev), devMinor(dev))
	}
	big := uint64(8)<<8 | uint64(0x123)&0xff | (uint64(0x123)&^0xff)<<12
	if devMajor(big) != 8 || devMinor(big) != 0x123 {
		t.Errorf("major %d minor %#x", devMajor(big), devMinor(big))
	}
}
