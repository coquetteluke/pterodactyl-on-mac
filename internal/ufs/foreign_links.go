// SPDX-License-Identifier: MIT

package ufs

import (
	"golang.org/x/sys/unix"
)

// GuardForeignLinks makes the filesystem refuse to open, chown or chmod a file
// that has more than one name (hard link) unless it already belongs to uid,
// the account whose files this filesystem holds.
//
// Every path check in this package answers one question: is this name inside
// the root. A hard link makes that the wrong question. The name is inside the
// root and the file is somewhere else as well, and nothing about the path says
// so. Making a link takes neither ownership of the file nor permission to read
// it on darwin, so a server that is a host process can give any file on the
// same volume a name in its own directory and wait for wings, which is root,
// to read it out, write over it or chown it to the server's account.
//
// In a container the same call can only reach the files in the volume, which
// is why upstream needs none of this, and Linux refuses the link itself when
// fs.protected_hardlinks is set. The caller decides when this is needed.
//
// A file with one name has that name inside the root, or it could not have
// been reached. A file the account owns gives it nothing it did not have. What
// is left, more than one name and somebody else's, is the case refused.
//
// This is a check of a link that is sitting still. A process that makes and
// removes the link while wings is looking can get past it, since the link
// count is only what it was at the moment it was read. What stops the link
// being made at all is the sandbox profile; see internal/sandbox.
func (fs *UnixFS) GuardForeignLinks(uid int) {
	fs.guardLinks = true
	fs.linkOwner = uint32(uid)
}

// foreignLink reports whether st describes a file that has names this
// filesystem cannot account for and that the account it serves does not own.
// Directories cannot be linked and always have a link count above one.
func (fs *UnixFS) foreignLink(st *unix.Stat_t) bool {
	return fs.guardLinks &&
		st.Mode&unix.S_IFMT != unix.S_IFDIR &&
		st.Nlink > 1 &&
		st.Uid != fs.linkOwner
}

// ForeignLink reports whether info describes a file that GuardForeignLinks
// refuses, so that callers walking a directory can leave it out rather than
// fail on it.
func (fs *UnixFS) ForeignLink(info FileInfo) bool {
	st, ok := info.Sys().(*unix.Stat_t)
	return ok && fs.foreignLink(st)
}

// checkNamedLink returns ErrForeignLink if name beneath dirfd is a foreign
// link, for the calls that act on a name rather than on an open file. atFlags
// are those of the call being guarded, so that the same file is looked at.
func (fs *UnixFS) checkNamedLink(dirfd int, name string, atFlags int) error {
	if !fs.guardLinks {
		return nil
	}
	var st unix.Stat_t
	if err := ignoringEINTR(func() error {
		return unix.Fstatat(dirfd, name, &st, atFlags&AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		return err
	}
	if fs.foreignLink(&st) {
		return ErrForeignLink
	}
	return nil
}
