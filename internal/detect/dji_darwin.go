package detect

import (
	"os"
	"syscall"
	"time"
)

// sfDataless marks an APFS file whose contents were evicted to iCloud.
const sfDataless = 0x40000000

func fileCtime(info os.FileInfo) time.Time {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.ModTime()
	}
	return time.Unix(st.Ctimespec.Sec, st.Ctimespec.Nsec)
}

// isEvicted reports iCloud placeholders; reading them would force a download.
func isEvicted(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Flags&sfDataless != 0 || (st.Blocks == 0 && info.Size() > 0)
}
