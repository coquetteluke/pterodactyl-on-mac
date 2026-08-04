// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: Copyright (c) 2024 Matthew Penner

//go:build linux

package ufs

import "golang.org/x/sys/unix"

// _openat2 is a wonderful syscall that supersedes the `openat` syscall. It has
// improved validation and security characteristics that weren't available or
// considered when `openat` was originally implemented. As such, it is only
// present in Kernel 5.6 and above.
//
// This method should never be directly called, use `openat` instead.
func (fs *UnixFS) _openat2(dirfd int, name string, flag, mode uint64) (int, error) {
	// Ensure the O_CLOEXEC flag is set.
	// Go sets this when using the os package, but since we are directly using
	// the unix package we need to set it ourselves.
	if flag&O_CLOEXEC == 0 {
		flag |= O_CLOEXEC
	}
	// Ensure the O_LARGEFILE flag is set.
	// Go sets this for unix.Open, unix.Openat, but not unix.Openat2.
	if flag&O_LARGEFILE == 0 {
		flag |= O_LARGEFILE
	}
	fd, err := unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags: flag,
		Mode:  mode,
		// This is the bread and butter of preventing a symlink escape, without
		// this option, we have to handle path validation fully on our own.
		//
		// This is why using Openat2 over Openat is preferred if available.
		Resolve: unix.RESOLVE_BENEATH,
	})
	switch {
	case err == nil:
		return fd, nil
	case err == unix.EINTR:
		return fd, err
	case err == unix.EAGAIN:
		return fd, err
	default:
		return fd, ensurePathError(err, "openat2", name)
	}
}
