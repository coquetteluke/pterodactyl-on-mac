//go:build !linux

package docker

// cgroupBurstSupported is false everywhere but Linux. On macOS the docker engine
// runs in its own Linux VM, so the pid Docker reports belongs to that VM and the
// host has neither a /proc entry for it nor a cgroup hierarchy to write. Trying
// anyway fails on every container start, and the failure looks like an outdated
// kernel rather than an environment that was never going to support it.
const cgroupBurstSupported = false
