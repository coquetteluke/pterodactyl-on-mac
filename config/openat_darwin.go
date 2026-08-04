//go:build darwin

package config

// openat2Supported reports whether the platform has an openat2 syscall at all.
//
// XNU has no openat2 and no RESOLVE_BENEATH equivalent, so wings falls back to
// the plain openat path, which validates each component in userspace. That is
// the same code path used on Linux kernels older than 5.6.
const openat2Supported = false

// probeOpenat2 is unreachable on darwin because openat2Supported gates it.
func probeOpenat2() bool { return false }
