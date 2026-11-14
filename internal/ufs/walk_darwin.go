// SPDX-License-Identifier: MIT

//go:build darwin

package ufs

import (
	"os"

	"golang.org/x/sys/unix"
)

// readDir reads a directory through an already-open descriptor.
//
// Linux does this with raw getdents calls into a caller-supplied scratch
// buffer. darwin has no getdents: golang.org/x/sys/unix emulates
// Getdirentries with fdopendir/readdir_r/closedir, re-opening the directory
// and skipping N entries on every call, which turns a single directory read
// into O(n^2) work. That matters here -- a Minecraft world has tens of
// thousands of region files, and this function backs both backups and the
// panel's file manager.
//
// Instead we open the directory stream once via the os package (which uses
// fdopendir directly) and read it in a single pass. The scratch buffer is
// unused on this platform; the parameter is kept for signature parity.
func (fs *UnixFS) readDir(fd int, name, relative string, _ []byte) ([]DirEntry, error) {
	// os.File takes ownership of the descriptor it wraps and will close it,
	// but the caller still needs its fd afterwards to resolve children with
	// *at syscalls. Hand os a duplicate instead.
	//
	// Close-on-exec, which plain dup does not set: a server started while a
	// directory is being read must not inherit a descriptor for it.
	dupfd, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, ensurePathError(err, "dup", name)
	}
	// Dup shares the file description, and therefore the directory offset,
	// with the original descriptor. Rewind so a reused fd still lists in full.
	if _, err := unix.Seek(dupfd, 0, 0); err != nil {
		_ = unix.Close(dupfd)
		return nil, ensurePathError(err, "lseek", name)
	}

	// name is the directory's last path element or ".", so to the os package
	// it is a label for error messages and not a path: resolved, it would be
	// looked up in the working directory. ReadDir does not resolve it. An
	// entry the filesystem leaves untyped (DT_UNKNOWN: some network and FUSE
	// volumes, never APFS or HFS+) is described with fstatat on the
	// descriptor.
	//
	// That is so from Go 1.25.8 and 1.26.1, and go.mod requires more. Earlier
	// releases called lstat on name/entry, which drops the entry when nothing
	// is there and gives it the type of whatever is. Info on the entries
	// ReadDir returns still goes by path, so only Name and Type are used.
	// TestOSDescribesEntriesThroughDescriptor holds the os package to this.
	f := os.NewFile(uintptr(dupfd), name)
	if f == nil {
		_ = unix.Close(dupfd)
		return nil, ensurePathError(unix.EBADF, "fdopendir", name)
	}
	defer f.Close()

	infos, err := f.ReadDir(-1)
	if err != nil {
		return nil, ensurePathError(err, "readdir", name)
	}

	entries := make([]DirEntry, 0, len(infos))
	for _, info := range infos {
		childName := info.Name()
		// The os package already filters "." and ".." out, but the Linux path
		// does it explicitly and cheap defensiveness costs nothing here.
		if isDotOrDotDot([]byte(childName)) {
			continue
		}

		mt, err := fs.modeTypeFromDirEntry(info, fd, childName)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fs.newDirent(fd, name, relative, childName, mt))
	}
	return entries, nil
}

// modeTypeFromDirEntry is the darwin counterpart to modeTypeFromDirent. It
// narrows an os.DirEntry to the same mode-type bits the Linux path produces.
//
// An entry the filesystem left untyped (DT_UNKNOWN, DT_WHT) does not arrive
// here untyped, as it does in modeTypeFromDirent on Linux: the os package has
// already described it through the descriptor, see readDir. ModeIrregular is
// for a file Go has no type for. It reports none on darwin today; if it starts
// to, ask the descriptor instead of passing the entry on as irregular.
func (fs *UnixFS) modeTypeFromDirEntry(de os.DirEntry, fd int, name string) (FileMode, error) {
	t := de.Type()
	if t&ModeIrregular != 0 {
		return fs.modeType(fd, name)
	}
	return t & ModeType, nil
}
