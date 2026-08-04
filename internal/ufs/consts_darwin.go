// SPDX-License-Identifier: MIT

//go:build darwin

package ufs

const (
	// O_LARGEFILE does not exist on darwin: every file descriptor is already
	// 64-bit capable, so the flag is inert. Zero keeps the shared bit-twiddling
	// in _openat a no-op rather than corrupting the flag set.
	O_LARGEFILE = 0

	// AT_EMPTY_PATH does not exist on darwin. It is only ever used by this
	// package as the dirfd argument to _openat for fs.basePath, which is always
	// absolute -- POSIX requires the dirfd to be ignored in that case, so the
	// value is never actually interpreted. We keep the Linux numeric value so
	// the two platforms stay bit-identical if it is ever used as a real flag.
	AT_EMPTY_PATH = 0x1000
)

// utimeOmit tells utimensat to leave one of the two timestamps untouched.
//
// golang.org/x/sys/unix does not export UTIME_OMIT for darwin even though the
// platform supports it; macOS defines it as -2 in <sys/stat.h>, and darwin's
// UtimesNanoAt (via syscall_bsd.go) honors it.
const utimeOmit = -2

// openat2Available reports whether this platform has an openat2 syscall to
// call at all. XNU does not, so UnixFS always falls back to openat.
const openat2Available = false
