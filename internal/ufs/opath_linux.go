// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: Copyright (c) 2024 Matthew Penner

//go:build linux

package ufs

import (
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// pathOnlyOpen reports whether flag asks for a descriptor that only identifies
// a file so that it can be described, and is never read from or written to.
func pathOnlyOpen(flag int) bool {
	return flag&unix.O_PATH != 0
}

// statFollow describes the file the symbolic link name refers to, resolving the
// link from the root of the filesystem so that it cannot lead outside of it.
func (fs *UnixFS) statFollow(name string) (FileInfo, error) {
	rel, err := fs.unsafePath(name)
	if err != nil {
		return nil, err
	}
	rootfd, err := fs._openat(AT_EMPTY_PATH, fs.basePath, O_DIRECTORY|O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootfd)

	fd, err := fs.openatResolved(rootfd, rel, unix.O_PATH, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)

	var s fileStat
	if err := ignoringEINTR(func() error {
		return unix.Fstat(fd, &s.sys)
	}); err != nil {
		return nil, ensurePathError(err, "stat", name)
	}
	fillFileStatFromSys(&s, filepath.Base(rel))
	return &s, nil
}

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
