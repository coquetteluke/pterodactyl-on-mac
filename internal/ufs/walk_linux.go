// SPDX-License-Identifier: BSD-2-Clause

// Some code in this file was derived from https://github.com/karrick/godirwalk.

//go:build linux

package ufs

import (
	"bytes"
	"reflect"
	"unsafe"

	"golang.org/x/sys/unix"
)

// nameOffset is a compile time constant
const nameOffset = int(unsafe.Offsetof(unix.Dirent{}.Name))

func nameFromDirent(de *unix.Dirent) (name []byte) {
	// Because this GOOS' syscall.Dirent does not provide a field that specifies
	// the name length, this function must first calculate the max possible name
	// length, and then search for the NULL byte.
	ml := int(de.Reclen) - nameOffset

	// Convert syscall.Dirent.Name, which is array of int8, to []byte, by
	// overwriting Cap, Len, and Data slice header fields to the max possible
	// name length computed above, and finding the terminating NULL byte.
	//
	// TODO: is there an alternative to the deprecated SliceHeader?
	// SliceHeader was mainly deprecated due to it being misused for avoiding
	// allocations when converting a byte slice to a string, ref;
	// https://go.dev/issue/53003
	sh := (*reflect.SliceHeader)(unsafe.Pointer(&name))
	sh.Cap = ml
	sh.Len = ml
	sh.Data = uintptr(unsafe.Pointer(&de.Name[0]))

	if index := bytes.IndexByte(name, 0); index >= 0 {
		// Found NULL byte; set slice's cap and len accordingly.
		sh.Cap = index
		sh.Len = index
		return
	}

	// NOTE: This branch is not expected, but included for defensive
	// programming, and provides a hard stop on the name based on the structure
	// field array size.
	sh.Cap = len(de.Name)
	sh.Len = sh.Cap
	return
}

// modeTypeFromDirent converts a syscall defined constant, which is in purview
// of OS, to a constant defined by Go, assumed by this project to be stable.
//
// When the syscall constant is not recognized, this function falls back to a
// Stat on the file system.
func (fs *UnixFS) modeTypeFromDirent(de *unix.Dirent, fd int, name string) (FileMode, error) {
	switch de.Type {
	case unix.DT_REG:
		return 0, nil
	case unix.DT_DIR:
		return ModeDir, nil
	case unix.DT_LNK:
		return ModeSymlink, nil
	case unix.DT_CHR:
		return ModeDevice | ModeCharDevice, nil
	case unix.DT_BLK:
		return ModeDevice, nil
	case unix.DT_FIFO:
		return ModeNamedPipe, nil
	case unix.DT_SOCK:
		return ModeSocket, nil
	default:
		// If syscall returned unknown type (e.g., DT_UNKNOWN, DT_WHT), then
		// resolve actual mode by reading file information.
		return fs.modeType(fd, name)
	}
}

func (fs *UnixFS) readDir(fd int, name, relative string, b []byte) ([]DirEntry, error) {
	scratchBuffer := b
	if scratchBuffer == nil || len(scratchBuffer) < minimumScratchBufferSize {
		scratchBuffer = newScratchBuffer()
	}

	var entries []DirEntry
	var workBuffer []byte

	var sde unix.Dirent
	for {
		if len(workBuffer) == 0 {
			n, err := unix.Getdents(fd, scratchBuffer)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return nil, ensurePathError(err, "getdents", name)
			}
			if n <= 0 {
				// end of directory: normal exit
				return entries, nil
			}
			workBuffer = scratchBuffer[:n] // trim work buffer to number of bytes read
		}

		// "Go is like C, except that you just put `unsafe` all over the place".
		copy((*[unsafe.Sizeof(unix.Dirent{})]byte)(unsafe.Pointer(&sde))[:], workBuffer)
		workBuffer = workBuffer[sde.Reclen:] // advance buffer for next iteration through loop

		if sde.Ino == 0 {
			continue // inode set to 0 indicates an entry that was marked as deleted
		}

		nameSlice := nameFromDirent(&sde)
		if isDotOrDotDot(nameSlice) {
			continue
		}

		childName := string(nameSlice)
		mt, err := fs.modeTypeFromDirent(&sde, fd, childName)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fs.newDirent(fd, name, relative, childName, mt))
	}
}
