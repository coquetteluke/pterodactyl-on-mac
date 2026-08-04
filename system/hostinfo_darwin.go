//go:build darwin

package system

import (
	"golang.org/x/sys/unix"
)

// hostMemoryBytes reports installed physical memory.
//
// The Docker path normally supplies this from the daemon's own view of the
// host. There is no daemon here, so ask the kernel directly.
func hostMemoryBytes() int64 {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(v)
}

// hostOSName reports a human-readable OS name.
//
// The Linux path reads /etc/os-release, which macOS does not have, so the
// equivalent comes from sysctl. kern.osproductversion is the user-facing
// version ("15.2") rather than the Darwin kernel version.
func hostOSName() string {
	name := "macOS"
	if v, err := unix.Sysctl("kern.osproductversion"); err == nil && v != "" {
		name += " " + v
	}
	return name
}
