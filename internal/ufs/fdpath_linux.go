// SPDX-License-Identifier: MIT

//go:build linux

package ufs

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

// resolveBasePath returns the base path used for comparing against fully
// resolved paths returned by the kernel. On Linux, procfs already hands back a
// real path and the configured base is used verbatim.
func resolveBasePath(basePath string) string {
	return basePath
}

// resolveFdPath returns the fully symlink-resolved path that fd refers to, by
// reading it back out of procfs.
//
// The second return value is a non-fatal error that openat reports to its
// caller only after the sandbox check has still run; it is set when the final
// component of the path does not exist. The third is a fatal lookup failure.
func (fs *UnixFS) resolveFdPath(fd int) (string, error, error) {
	finalPath, err := filepath.EvalSymlinks(filepath.Join("/proc/self/fd/", strconv.Itoa(fd)))
	if err != nil {
		if !errors.Is(err, ErrNotExist) {
			return "", nil, fmt.Errorf("failed to evaluate symlink: %w", convertErrorType(err))
		}

		// The target of one of the symlinks (EvalSymlinks is recursive)
		// does not exist. So get the path that does not exist and use
		// that for further validation instead.
		var pErr *PathError
		if !errors.As(err, &pErr) {
			return "", nil, fmt.Errorf("failed to evaluate symlink: %w", convertErrorType(err))
		}

		// Update the final path to whatever directory or path didn't exist while
		// recursing any symlinks, and ensure the error is wrapped correctly.
		return pErr.Path, convertErrorType(err), nil
	}
	return finalPath, nil, nil
}
