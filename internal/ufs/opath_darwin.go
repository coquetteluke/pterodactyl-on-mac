// SPDX-License-Identifier: MIT

//go:build darwin

package ufs

import "golang.org/x/sys/unix"

// openPathOnly stands in for Linux's O_PATH, which darwin does not have.
//
// It is used to open the target of a symbolic link purely so that it can be
// described with fstat once the kernel has confirmed where it resolved to.
// Without O_PATH the target really is opened, so:
//
//   - O_NONBLOCK keeps the open from blocking forever when the target is a
//     FIFO with no writer. The descriptor is never read, so it is never
//     switched back to blocking mode.
//   - O_EVTONLY is the closest thing XNU has to a "reference only" open: the
//     descriptor does not keep the volume busy. It is also what lets openat
//     tell this request apart from an ordinary read-only open.
//
// Two things O_PATH allows are not possible here: a link to a socket cannot be
// described (open fails with EOPNOTSUPP), and neither can a link to a file the
// daemon is not permitted to read.
const openPathOnly = unix.O_RDONLY | unix.O_EVTONLY | unix.O_NONBLOCK

// fchmodNoFollow changes the mode of name without following it if it is a
// symbolic link.
//
// On Linux this exists because fchmodat only accepts AT_SYMLINK_NOFOLLOW on
// kernels with fchmodat2. darwin's fchmodat has always accepted it, so there is
// nothing to work around and no procfs to go through. Linux refuses to change
// the mode of a symbolic link itself; do the same here so both platforms
// report the same thing.
func fchmodNoFollow(dirfd int, name string, mode uint32) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.EOPNOTSUPP
	}
	return unix.Fchmodat(dirfd, name, mode, unix.AT_SYMLINK_NOFOLLOW)
}
