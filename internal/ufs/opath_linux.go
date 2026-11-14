// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: Copyright (c) 2024 Matthew Penner

//go:build linux

package ufs

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// openPathOnly is the open flag used to obtain a descriptor that only
// identifies a file so that it can be described, and is never read from or
// written to.
const openPathOnly = unix.O_PATH

// fchmodNoFollow changes the mode of name without following it if it is a
// symbolic link, without needing fchmodat2.
func fchmodNoFollow(dirfd int, name string, mode uint32) error {
	fd, err := unix.Openat(dirfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	// Linux does not support changing the mode of a symbolic link.
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.EOPNOTSUPP
	}
	// The descriptor was opened with O_PATH, which fchmod does not accept, so
	// change the mode through its entry in /proc, which refers to the file that
	// was opened.
	return unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), mode)
}
