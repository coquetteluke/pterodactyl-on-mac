//go:build darwin

package sandbox

import "os"

// ExecPath is the sandbox launcher. It is referenced by absolute path rather
// than found on PATH, so that what confines a server cannot be changed by the
// environment wings happens to inherit.
const ExecPath = "/usr/bin/sandbox-exec"

// Supported reports whether filesystem confinement is available.
//
// sandbox-exec has carried a deprecation notice for many releases while
// remaining the interface to a sandbox macOS itself relies on, so its presence
// is checked at runtime rather than assumed.
func Supported() bool {
	st, err := os.Stat(ExecPath)
	return err == nil && !st.IsDir()
}
