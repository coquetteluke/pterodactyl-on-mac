//go:build !darwin

package serveruser

// Per-server accounts exist to replace the isolation that containers provide.
// On platforms where Wings can use Docker there is nothing to replace, so these
// are stubs rather than a second implementation to keep in step.

func ensure(uuid string) (Account, error) {
	return Account{}, ErrNotSupported
}

func lookup(name string) (Account, bool, error) {
	return Account{}, false, ErrNotSupported
}
