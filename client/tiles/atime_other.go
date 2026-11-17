//go:build !linux

package tiles

import (
	"io/fs"
	"time"
)

func accessTime(info fs.FileInfo) time.Time { return info.ModTime() }
