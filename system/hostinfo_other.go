//go:build !darwin

package system

// On platforms where Wings runs the Docker environment, the daemon supplies
// both of these and these fallbacks are never consulted.

func hostMemoryBytes() int64 { return 0 }

func hostOSName() string { return "" }
