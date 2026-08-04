// SPDX-License-Identifier: BSD-2-Clause

// Some code in this file was derived from https://github.com/karrick/godirwalk.

//go:build unix

package ufs

import (
	"fmt"
	iofs "io/fs"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

type WalkDiratFunc func(dirfd int, name, relative string, d DirEntry, err error) error

func (fs *UnixFS) WalkDirat(dirfd int, name string, fn WalkDiratFunc) error {
	info, err := fs.Lstatat(dirfd, name)
	if err != nil {
		err = fn(dirfd, name, ".", nil, err)
	} else {
		b := newScratchBuffer()
		err = fs.walkDir(b, dirfd, name, ".", iofs.FileInfoToDirEntry(info), fn)
	}
	if err == SkipDir || err == SkipAll {
		return nil
	}
	return err
}

func (fs *UnixFS) walkDir(b []byte, parentfd int, name, relative string, d DirEntry, walkDirFn WalkDiratFunc) error {
	if err := walkDirFn(parentfd, name, relative, d, nil); err != nil || !d.IsDir() {
		if err == SkipDir && d.IsDir() {
			// Successfully skipped directory.
			err = nil
		}
		return err
	}

	dirfd, err := fs.openat(parentfd, name, O_DIRECTORY|O_RDONLY, 0)
	if dirfd != 0 {
		defer unix.Close(dirfd)
	}
	if err != nil {
		return err
	}

	dirs, err := fs.readDir(dirfd, name, relative, b)
	if err != nil {
		// Second call, to report ReadDir error.
		err = walkDirFn(dirfd, name, relative, d, err)
		if err != nil {
			if err == SkipDir && d.IsDir() {
				err = nil
			}
			return err
		}
	}

	for _, d1 := range dirs {
		name := d1.Name()
		// This fancy logic ensures that if we start walking from a subdirectory
		// that we don't make the path relative to the root of the filesystem.
		//
		// For example, if we walk from the root of a filesystem, relative would
		// be "." and path.Join would end up just returning name. But if relative
		// was a subdirectory, relative could be "dir" and path.Join would make
		// it "dir/child" even though we are walking starting at dir.
		var rel string
		if relative == "." {
			rel = name
		} else {
			rel = path.Join(relative, name)
		}
		if err := fs.walkDir(b, dirfd, name, rel, d1, walkDirFn); err != nil {
			if err == SkipDir {
				break
			}
			return err
		}
	}

	return nil
}

// ReadDirMap .
// TODO: document
func ReadDirMap[T any](fs *UnixFS, path string, fn func(DirEntry) (T, error)) ([]T, error) {
	dirfd, name, closeFd, err := fs.safePath(path)
	defer closeFd()
	if err != nil {
		return nil, err
	}
	fd, err := fs.openat(dirfd, name, O_DIRECTORY|O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)

	entries, err := fs.readDir(fd, ".", path, nil)
	if err != nil {
		return nil, err
	}

	out := make([]T, len(entries))
	for i, e := range entries {
		idx := i
		e := e
		v, err := fn(e)
		if err != nil {
			return nil, err
		}
		out[idx] = v
	}

	return out, nil
}

// modeType returns the mode type of the file system entry identified by
// osPathname by calling os.LStat function, to intentionally not follow symbolic
// links.
//
// Even though os.LStat provides all file mode bits, we want to ensure same
// values returned to caller regardless of whether we obtained file mode bits
// from syscall or stat call. Therefore, mask out the additional file mode bits
// that are provided by stat but not by the syscall, so users can rely on their
// values.
func (fs *UnixFS) modeType(dirfd int, name string) (FileMode, error) {
	fi, err := fs.Lstatat(dirfd, name)
	if err != nil {
		return 0, fmt.Errorf("ufs: error finding mode type for %s during readDir: %w", name, err)
	}
	return fi.Mode() & ModeType, nil
}

var minimumScratchBufferSize = os.Getpagesize()

func newScratchBuffer() []byte {
	return make([]byte, minimumScratchBufferSize)
}

// readDir is defined per-GOOS: Linux reads the directory with raw getdents
// calls, darwin goes through fdopendir. See walk_linux.go and walk_darwin.go.

// newDirent builds a directory entry rooted at the still-open dirfd. Both
// platform readDir implementations funnel through here so the resulting
// entries resolve children via *at syscalls rather than by re-walking a path.
func (fs *UnixFS) newDirent(fd int, name, relative, childName string, mt FileMode) *dirent {
	var rel string
	if relative == "." {
		rel = name
	} else {
		rel = path.Join(relative, childName)
	}
	return &dirent{dirfd: fd, name: childName, path: rel, modeType: mt, fs: fs}
}

// isDotOrDotDot reports whether the raw directory entry name is "." or "..",
// both of which are skipped when building entries.
func isDotOrDotDot(name []byte) bool {
	n := len(name)
	return n == 0 || (name[0] == '.' && (n == 1 || (n == 2 && name[1] == '.')))
}

// dirent stores the name and file system mode type of discovered file system
// entries.
type dirent struct {
	dirfd    int
	name     string
	path     string
	modeType FileMode

	fs *UnixFS
}

func (de dirent) Name() string {
	return de.name
}

func (de dirent) IsDir() bool {
	return de.modeType&ModeDir != 0
}

func (de dirent) Type() FileMode {
	return de.modeType
}

func (de dirent) Info() (FileInfo, error) {
	if de.fs == nil {
		return nil, nil
	}
	return de.fs.Lstatat(de.dirfd, de.name)
	// return de.fs.Lstat(de.path)
}

func (de dirent) Open() (File, error) {
	if de.fs == nil {
		return nil, nil
	}
	return de.fs.OpenFileat(de.dirfd, de.name, O_RDONLY, 0)
	// return de.fs.OpenFile(de.path, O_RDONLY, 0)
}

// reset releases memory held by entry err and name, and resets mode type to 0.
func (de *dirent) reset() {
	de.name = ""
	de.path = ""
	de.modeType = 0
	de.dirfd = 0
}
