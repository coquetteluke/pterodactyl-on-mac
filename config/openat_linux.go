//go:build linux

package config

import (
	"github.com/apex/log"
	"golang.org/x/sys/unix"
)

// openat2Supported reports whether the platform has an openat2 syscall at all.
const openat2Supported = true

// probeOpenat2 checks whether the running kernel actually implements openat2,
// which landed in Linux 5.6.
func probeOpenat2() bool {
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &unix.OpenHow{})
	if err != nil {
		log.WithError(err).Warn("error occurred while checking for openat2 support, falling back to openat")
		return false
	}
	_ = unix.Close(fd)
	return true
}
