// SPDX-License-Identifier: MIT

//go:build darwin

package ufs

import (
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// resolveBasePath returns the base path used for comparing against fully
// resolved paths returned by the kernel.
//
// fcntl(F_GETPATH) always reports the real path, so a base that traverses a
// symlink would never match it. That is not a hypothetical on macOS: /var,
// /tmp and /etc are all firmlinks into /private, so a wings data directory of
// /var/lib/pterodactyl comes back from the kernel as
// /private/var/lib/pterodactyl and every single path check would fail closed.
//
// If the base does not exist yet, fall back to the configured value; wings
// creates the directory before any file operation reaches this code.
func resolveBasePath(basePath string) string {
	resolved, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		return basePath
	}
	return strings.TrimSuffix(resolved, "/")
}

// maxPathLen matches darwin's MAXPATHLEN, the buffer size F_GETPATH requires.
const maxPathLen = 1024

// resolveFdPath returns the fully symlink-resolved path that fd refers to.
//
// Linux reads this out of /proc/self/fd. darwin has no procfs; the equivalent
// is fcntl(F_GETPATH), which fills a MAXPATHLEN buffer with the resolved path
// of an open descriptor.
//
// The result is translated back into configured-base terms so that the caller
// can compare it against fs.basePath and so error messages quote the path the
// operator configured rather than its /private twin.
//
// The second return value is a non-fatal error, kept for parity with the Linux
// implementation; F_GETPATH resolves the whole path at once and has no partial
// case, so it is always nil here. The third is a fatal lookup failure.
func (fs *UnixFS) resolveFdPath(fd int) (string, error, error) {
	buf := make([]byte, maxPathLen)
	// syscall.Syscall is used rather than unix.FcntlInt because F_GETPATH
	// takes a buffer pointer. Passing that pointer through FcntlInt's int
	// argument would hide it from the garbage collector; the compiler and go
	// vet both recognise the unsafe.Pointer-to-uintptr conversion in a
	// syscall.Syscall argument list and keep the buffer alive across the call.
	_, _, errno := syscall.Syscall(
		syscall.SYS_FCNTL,
		uintptr(fd),
		uintptr(unix.F_GETPATH),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	runtime.KeepAlive(buf)
	if errno != 0 {
		return "", nil, ensurePathError(errno, "fcntl", "F_GETPATH")
	}

	// The kernel writes a NUL-terminated C string into the buffer.
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	path := string(buf[:n])

	// Map the real path back onto the configured base so the caller's prefix
	// check works. A path outside the base is returned untouched, which is
	// exactly what makes the check fail and the escape get rejected.
	if fs.realBasePath != "" && fs.realBasePath != fs.basePath {
		if path == fs.realBasePath {
			return fs.basePath, nil, nil
		}
		if strings.HasPrefix(path, fs.realBasePath+"/") {
			return fs.basePath + strings.TrimPrefix(path, fs.realBasePath), nil, nil
		}
	}
	return path, nil, nil
}
