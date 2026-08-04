// SPDX-License-Identifier: MIT

//go:build darwin

package ufs

import "golang.org/x/sys/unix"

// _openat2 has no darwin equivalent.
//
// The security value of openat2 comes from RESOLVE_BENEATH, which makes the
// kernel itself guarantee that resolution never escapes the starting directory.
// XNU offers no such flag, so on darwin we always take the `openat` path in
// `openat`, which performs the symlink validation in userspace -- the same path
// wings already uses on Linux kernels older than 5.6.
//
// config.UseOpenat2 is hardcoded to false on darwin, so this is never called.
// It exists only to satisfy the shared call site in fs_unix.go, and returns
// ENOSYS so that a future refactor which reaches it fails loudly rather than
// silently resolving paths without sandboxing.
func (fs *UnixFS) _openat2(dirfd int, name string, flag, mode uint64) (int, error) {
	return -1, ensurePathError(unix.ENOSYS, "openat2", name)
}
