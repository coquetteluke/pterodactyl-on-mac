// SPDX-License-Identifier: MIT

//go:build linux

package ufs

import "golang.org/x/sys/unix"

const (
	// O_LARGEFILE allows 64-bit offsets on 32-bit Linux. Go's os package sets
	// this implicitly, but we call into the unix package directly.
	O_LARGEFILE = unix.O_LARGEFILE

	// AT_EMPTY_PATH permits the *at syscalls to operate on the dirfd itself
	// when the supplied path is empty.
	AT_EMPTY_PATH = unix.AT_EMPTY_PATH
)

// utimeOmit tells utimensat to leave one of the two timestamps untouched.
const utimeOmit = unix.UTIME_OMIT

// openat2Available reports whether this platform has an openat2 syscall to
// call at all. Whether the running kernel actually implements it is a separate
// question answered by config.UseOpenat2.
const openat2Available = true
