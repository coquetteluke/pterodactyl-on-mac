//go:build linux

package docker

// cgroupBurstSupported reports whether CFS burst can be applied from this host.
// Wings reaches the container's cgroup through /proc and /sys/fs/cgroup, both of
// which are the host's own kernel interfaces, so it only works when Wings runs on
// the same kernel as the containers.
const cgroupBurstSupported = true
