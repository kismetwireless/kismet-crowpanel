package tiles

import (
	"io/fs"
	"syscall"
	"time"
)

func accessTime(info fs.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atim.Unix())
	}
	return info.ModTime()
}
