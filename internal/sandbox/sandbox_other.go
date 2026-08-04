//go:build !darwin

package sandbox

// Filesystem confinement here is what a container's mount namespace provides,
// so there is nothing for this package to do.

const ExecPath = ""

// Supported reports whether filesystem confinement is available.
func Supported() bool { return false }
