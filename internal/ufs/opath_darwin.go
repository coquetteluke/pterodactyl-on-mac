// SPDX-License-Identifier: MIT

//go:build darwin

package ufs

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// pathOnlyOpen reports whether flag asks for a descriptor that only identifies
// a file. That is O_PATH, which darwin does not have, so nothing does.
func pathOnlyOpen(int) bool {
	return false
}

// maxLinkDepth is how many symbolic links statFollow resolves before giving
// up, the same number the kernel allows.
const maxLinkDepth = 32

// statFollow describes the file the symbolic link name refers to, without
// letting the link lead outside of the filesystem.
//
// Linux does this by opening the target with O_PATH, which reaches a file
// without running its open routine, checking where that landed, and calling
// fstat. Without O_PATH the only way to get a descriptor for the target is to
// really open it, and a link can point anywhere: at a FIFO, which would make
// wings its reader, or at a device, whose driver would be opened as whoever
// wings runs as, all before it is known to be outside the root.
//
// So the chain of links is followed here instead. Each step opens only the
// directory holding the next name, which the kernel refuses unless it is a
// directory and which is then confirmed to be inside the root, and describes
// that name without following it. A link's target is read and resolved
// against the directory it is in, and has to stay inside the root to be looked
// at at all.
//
// The target is cleaned as written, where the kernel would resolve each part
// in turn. The two differ for a target that goes up through a linked
// directory (dir-link/../x) or ends in a slash: such a link may be reported as
// missing, or as the file its text names. Either way the answer is about
// something inside the root, and anything that goes on to open the path has
// the kernel resolve it and is checked again.
func (fs *UnixFS) statFollow(name string) (FileInfo, error) {
	rel, err := fs.unsafePath(name)
	if err != nil {
		return nil, err
	}
	rootfd, err := fs._openat(AT_EMPTY_PATH, fs.basePath, O_DIRECTORY|O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootfd)

	cur := rel
	// One more step than there are links allowed: each link takes a step to
	// read, and what the last one points at takes one to describe.
	for range maxLinkDepth + 1 {
		s, target, err := fs.describeOrReadLink(rootfd, cur)
		if err != nil {
			return nil, ensurePathError(err, "stat", name)
		}
		if s != nil {
			fillFileStatFromSys(s, filepath.Base(rel))
			return s, nil
		}
		if !fs.unsafeIsPathInsideOfBase(target) {
			return nil, &PathError{Op: "stat", Path: name, Err: ErrBadPathResolution}
		}
		cur = strings.TrimPrefix(strings.TrimPrefix(target, fs.basePath), "/")
		if cur == "" {
			cur = "."
		}
	}
	// Too many links, which the kernel reports as ELOOP and callers already
	// treat as a path that does not resolve inside the root.
	return nil, &PathError{Op: "stat", Path: name, Err: ErrBadPathResolution}
}

// describeOrReadLink looks at rel, a path relative to rootfd, without following
// it. If it is not a symbolic link its description is returned. If it is, the
// cleaned absolute path it points at is returned instead, in terms of the
// configured base path.
func (fs *UnixFS) describeOrReadLink(rootfd int, rel string) (*fileStat, string, error) {
	dir, leaf := filepath.Split(rel)
	dirfd := rootfd
	if dir = strings.TrimSuffix(dir, "/"); dir != "" {
		// O_DIRECTORY is checked before anything is opened, and the result is
		// confirmed to be inside the root, links in the path included.
		fd, err := fs.openatResolved(rootfd, dir, O_DIRECTORY|O_RDONLY, 0)
		if err != nil {
			return nil, "", err
		}
		defer unix.Close(fd)
		dirfd = fd
	}

	var s fileStat
	if err := ignoringEINTR(func() error {
		return unix.Fstatat(dirfd, leaf, &s.sys, AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		return nil, "", err
	}
	if s.sys.Mode&unix.S_IFMT != unix.S_IFLNK {
		return &s, "", nil
	}

	target, err := fs.Readlinkat(dirfd, leaf)
	if err != nil {
		return nil, "", err
	}
	if !filepath.IsAbs(target) {
		// Relative to the directory the link is in, as the kernel has it.
		parent, _, err := fs.resolveFdPath(dirfd)
		if err != nil {
			return nil, "", err
		}
		target = filepath.Join(parent, target)
	}
	target = filepath.Clean(target)

	// A link may spell out the real path of a root that sits behind a
	// firmlink, /private/var for /var; that is still inside the root.
	if fs.realBasePath != "" && fs.realBasePath != fs.basePath {
		if target == fs.realBasePath {
			target = fs.basePath
		} else if strings.HasPrefix(target, fs.realBasePath+"/") {
			target = fs.basePath + strings.TrimPrefix(target, fs.realBasePath)
		}
	}
	return nil, target, nil
}

// fchmodNoFollow changes the mode of name without following it if it is a
// symbolic link.
//
// On Linux this exists because fchmodat only accepts AT_SYMLINK_NOFOLLOW on
// kernels with fchmodat2. darwin's fchmodat has always accepted it, so there is
// nothing to work around and no procfs to go through. Linux refuses to change
// the mode of a symbolic link itself; do the same here so both platforms
// report the same thing.
func fchmodNoFollow(dirfd int, name string, mode uint32) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return unix.EOPNOTSUPP
	}
	return unix.Fchmodat(dirfd, name, mode, unix.AT_SYMLINK_NOFOLLOW)
}
