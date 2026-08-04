//go:build !darwin

package netiso

import "emperror.dev/errors"

// Network isolation exists because macOS has no network namespaces. Everywhere
// else the Docker environment gives each server its own stack, so there is
// nothing for this package to do and the honest answer is to refuse rather
// than to pretend a policy is in force.

const (
	AnchorName = "wings"
	AnchorPath = ""
)

var ErrAnchorNotReferenced = errors.Sentinel("netiso: unsupported on this platform")

// Supported reports whether this platform can enforce network isolation.
func Supported() bool { return false }

// Available reports whether isolation can be applied right now.
func Available() bool { return false }

// Apply always fails here. Callers gate on Supported first, so reaching this
// means isolation was asked for on a platform that uses containers instead.
func Apply(rules []Rule) error {
	if len(rules) == 0 {
		return nil
	}
	return errors.New("netiso: network isolation is only implemented on darwin; use the Docker environment's networking elsewhere")
}

// Clear is a no-op because nothing was ever loaded.
func Clear() error { return nil }
