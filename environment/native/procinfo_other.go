//go:build !darwin

package native

import (
	"time"

	"emperror.dev/errors"
	"golang.org/x/sys/unix"
)

// The native environment is currently implemented for darwin only, where it
// exists because macOS cannot run Linux containers. On every other platform the
// Docker environment is both available and strictly better, so these stubs
// exist purely to keep the tree compiling rather than to be used.
//
// Porting this package to another OS means supplying real process accounting
// here; nothing in the rest of the package is darwin-specific.

var errUnsupportedPlatform = errors.New("environment/native: the native process environment is only implemented on darwin")

type taskInfo struct {
	VirtualSize   uint64
	ResidentSize  uint64
	TotalUser     uint64
	TotalSystem   uint64
	ThreadsUser   uint64
	ThreadsSystem uint64
	ThreadNum     int32
}

func pidTaskInfo(pid int) (*taskInfo, error) {
	return nil, errUnsupportedPlatform
}

func pidStartTime(pid int) (time.Time, error) {
	return time.Time{}, errUnsupportedPlatform
}

func processIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return unix.Kill(pid, 0) == nil
}

func pidsInGroup(pgid int) ([]int, error) {
	return nil, errUnsupportedPlatform
}

func totalMemory() (uint64, error) {
	return 0, errUnsupportedPlatform
}
