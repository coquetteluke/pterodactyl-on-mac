package filesystem

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// CTime returns the time that the file/folder was created.
//
// The Linux implementation reads st_ctim, and notes in a TODO that this is
// wrong -- st_ctim is the inode change time, not the creation time. darwin
// does not have that problem: HFS+ and APFS record a real birth time, so we
// report that and fall back to the change time only if the filesystem left it
// unset (it is zero on some network mounts).
//
// Note the two packages spell these fields differently on darwin:
// x/sys/unix uses Btim/Ctim, while the standard library's syscall package uses
// Birthtimespec/Ctimespec. Both branches are kept for parity with the Linux
// implementation, which handles the same split.
func (s *Stat) CTime() time.Time {
	if st, ok := s.Sys().(*unix.Stat_t); ok {
		if st.Btim.Sec > 0 {
			return time.Unix(int64(st.Btim.Sec), int64(st.Btim.Nsec))
		}
		return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec))
	}
	if st, ok := s.Sys().(*syscall.Stat_t); ok {
		if st.Birthtimespec.Sec > 0 {
			return time.Unix(int64(st.Birthtimespec.Sec), int64(st.Birthtimespec.Nsec))
		}
		return time.Unix(int64(st.Ctimespec.Sec), int64(st.Ctimespec.Nsec))
	}
	return time.Time{}
}
