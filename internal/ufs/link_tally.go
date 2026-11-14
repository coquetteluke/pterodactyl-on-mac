// SPDX-License-Identifier: MIT

//go:build unix

package ufs

import (
	"cmp"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// LinkTally works out which of the files GuardForeignLinks refused during a
// walk are the account's own after all, and hands those over.
//
// The guard refuses a file that has more than one name and that the account
// does not own, because one of those names may be outside of the root. That is
// also what a server's own hard-linked files look like the first time it is
// given an account, or a new one: a package manager's store linked into a
// project, a build directory, a local clone of a repository. Every name is
// inside the root and the owner is whoever the files belonged to before.
// Refused, they stay with that owner. From then on they are left out of
// backups, cannot be read through wings at all, and cannot be written by the
// server.
//
// The link count is how many names a file has, and a walk of a directory meets
// every name the file has beneath it. Where the two agree the file has no name
// anywhere else. Nobody can be using it by a name the walk did not meet, and
// handing it over gives the account what handing over a file with one name
// does: its own data.
//
// Names counted while walking prove nothing. A name that is moved ahead of the
// walk is met twice, so one name inside the root and one outside can be made to
// look like two inside. So nothing is decided during the walk. Record keeps
// each refused name, and HandOver looks at them again once the walk is over,
// one file at a time:
//
//   - The file is opened once, and what it is, how many names it has and the
//     change of owner are all asked of that descriptor. The file that was
//     checked is the file that is handed over, whatever its names lead to by
//     then.
//   - Every recorded name is looked up again. The directory it is in is
//     reached from the root of the walk one component at a time, following no
//     symbolic link, so a directory that has been replaced by a link to
//     another does not turn one name into two.
//   - Names are told apart by the directory they are in, which is to say its
//     device and inode and not its path, and by their spelling. Two spellings
//     in one directory count as two only if the directory lists both: on a
//     volume that ignores case they can be one name.
//   - The names found are compared to the link count read from the descriptor
//     after they were looked up, and only if they are the same number is the
//     owner changed.
//
// For a file that is sitting still this is exact, and nothing the guard
// refused before is let through: a name outside of the root is a link that no
// name inside accounts for.
//
// What it cannot do is look at all of a file's names at one moment. No call
// does that. A process of the server's that is running while this happens,
// and that has a name in the root for a file from outside, can move that one
// name from place to place in step with the lookups, so that each of them
// finds it, and have a file that is somebody else's handed over. It does not
// take much luck. Tried on Linux, a process doing nothing else got there a few
// times in a hundred, and a server can be started as often as its owner
// likes.
//
// That takes nothing the same process did not need to get past the guard as it
// was, where Lchownat looks at a name and then acts on the name, and in the
// same trial was given a different file in between rather more often than
// that. Wings does all of this while the server is stopped, but it stops a
// server by its process group, and a process that has left the group is still
// there. What
// such a process cannot do under the sandbox profile is make the name for the
// outside file to begin with, and that is the control this relies on, as the
// guard does; see internal/sandbox. A name made while the profile was not in
// force can still be moved.
//
// A LinkTally is used for one walk and is not safe for concurrent use.
type LinkTally struct {
	fs *UnixFS

	files map[fileID]*talliedFile
	// order is files in the order the walk first met them.
	order []*talliedFile
	// dirs are the directories names were found in, relative to the root of
	// the walk, each kept once.
	dirs     []string
	dirIndex map[string]int32

	// size is roughly the memory the names take up. No new file is taken on
	// past admit, and nothing at all is kept past limit.
	size, admit, limit int

	// met counts every name recorded, so that the first of those left can be
	// told from the order they came in.
	met       int
	left      int
	first     string
	firstSeen int
	// unseen is how many of those left were not kept for want of room.
	unseen int
}

const (
	// tallyAdmit bounds what a tree full of links can make wings hold in
	// memory, which a server could otherwise choose the size of. It is room
	// for something under half a million names of ordinary length. Past it
	// the files already taken on still have their remaining names recorded,
	// up to tallyLimit, so that they can be finished: handed over, they are
	// not refused on the next walk, which then gets further.
	tallyAdmit = 64 << 20
	tallyLimit = 2 * tallyAdmit

	// What a file, a directory and a name are counted as on top of the length
	// of their names. These are estimates, and on the high side: measured, a
	// tree of two-name files held about four fifths of what it was counted
	// as.
	tallyFileCost = 128
	tallyDirCost  = 64
	tallyNameCost = 48

	// tallyOpenDirs is how many directories HandOver keeps open between files.
	// It is kept small because every one is a descriptor.
	tallyOpenDirs = 32

	// tallyListLimit is how many directory entries HandOver will read in all
	// to tell two spellings in one directory apart, after which such names are
	// left. A directory can be made as large as a server likes.
	tallyListLimit = 1 << 22
)

// fileID identifies a file or a directory on this machine for as long as it
// exists.
type fileID struct {
	dev, ino uint64
}

func idOf(st *unix.Stat_t) fileID {
	return fileID{dev: uint64(st.Dev), ino: st.Ino}
}

type talliedName struct {
	dir  int32
	name string
}

type talliedFile struct {
	id fileID
	// nlink is the link count when the walk last met the file.
	nlink uint64
	names []talliedName
	// seen is when the walk first met the file.
	seen int
}

// LinkTallyResult is what became of the names a LinkTally was given.
type LinkTallyResult struct {
	// Files is how many files were handed over, and Names how many names they
	// had between them.
	Files, Names int
	// Left is how many names were left as they were, and First is the one of
	// them that the walk met first.
	Left  int
	First string
	// Unseen is how many of the names left were not looked at, because there
	// were more names than are kept for one walk. Some of them may be handed
	// over by the next.
	Unseen int
}

// NewLinkTally returns a tally for one walk of the filesystem.
func (fs *UnixFS) NewLinkTally() *LinkTally {
	return &LinkTally{
		fs:       fs,
		files:    make(map[fileID]*talliedFile),
		dirIndex: make(map[string]int32),
		admit:    tallyAdmit,
		limit:    tallyLimit,
	}
}

// Record notes that name in the directory dirfd, found at the path relative to
// the root of the walk, was refused with ErrForeignLink.
func (t *LinkTally) Record(dirfd int, name, relative string) {
	t.met++
	var st unix.Stat_t
	if err := ignoringEINTR(func() error {
		return unix.Fstatat(dirfd, name, &st, AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		t.leave(relative)
		return
	}
	// Only regular files are handed over. A file is opened to be handed over,
	// and anything else either cannot be opened without following it or does
	// something when it is.
	if !t.fs.foreignLink(&st) || st.Mode&unix.S_IFMT != unix.S_IFREG {
		t.leave(relative)
		return
	}
	dir, ok := parentOf(relative, name)
	if !ok {
		t.leave(relative)
		return
	}

	id := idOf(&st)
	f := t.files[id]
	idx, known := t.dirIndex[dir]
	cost := tallyNameCost + len(name)
	if !known {
		cost += tallyDirCost + len(dir)
	}
	if f == nil {
		cost += tallyFileCost
	}
	if t.size+cost > t.limit || (f == nil && t.size+cost > t.admit) {
		// A file with a name that is not kept has fewer names recorded than
		// it has links, and is left for that.
		t.unseen++
		t.leave(relative)
		return
	}
	t.size += cost

	if !known {
		// Copied, as the name is below, so that what is kept is no more than
		// what was counted.
		dir = strings.Clone(dir)
		idx = int32(len(t.dirs))
		t.dirs = append(t.dirs, dir)
		t.dirIndex[dir] = idx
	}
	if f == nil {
		f = &talliedFile{id: id, seen: t.met}
		t.files[id] = f
		t.order = append(t.order, f)
	}
	f.nlink = uint64(st.Nlink)
	f.names = append(f.names, talliedName{dir: idx, name: strings.Clone(name)})
}

// leave counts a name that is not going to be looked at again.
func (t *LinkTally) leave(relative string) {
	if t.left == 0 {
		t.first, t.firstSeen = relative, t.met
	}
	t.left++
}

// parentOf returns the directory part of relative, which ends in name.
func parentOf(relative, name string) (string, bool) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return "", false
	}
	if relative == name {
		return ".", true
	}
	// "dir/name", with something for dir.
	cut := len(relative) - len(name) - 1
	if cut < 1 || relative[cut] != '/' || relative[cut+1:] != name {
		return "", false
	}
	return relative[:cut], true
}

// HandOver gives uid and gid every recorded file whose names are all beneath
// the directory name in dirfd, the root of the walk the names were recorded
// on, and reports what was done. A file that cannot be shown to be one of
// those is left as it is, whatever the reason, and that is not an error: it is
// where the file was before.
//
// The tally is empty afterwards.
func (t *LinkTally) HandOver(dirfd int, name string, uid, gid int) LinkTallyResult {
	res := LinkTallyResult{Left: t.left, First: t.first, Unseen: t.unseen}
	firstSeen := t.firstSeen
	leave := func(f *talliedFile) {
		if res.Left == 0 || f.seen < firstSeen {
			res.First, firstSeen = t.path(f.names[0]), f.seen
		}
		res.Left += len(f.names)
	}

	// Opened when the first file needs it. Most walks refuse nothing, and most
	// of what is refused has a name the walk never met.
	var v *linkVerifier
	defer func() {
		if v != nil {
			v.close()
		}
	}()
	for i, f := range t.order {
		// Fewer names than links were met, so there is a name somewhere the
		// walk did not go.
		if uint64(len(f.names)) < f.nlink {
			leave(f)
			continue
		}
		if v == nil {
			root, err := t.fs.openat(dirfd, name, O_DIRECTORY|O_RDONLY, 0)
			if err != nil {
				for _, f := range t.order[i:] {
					leave(f)
				}
				break
			}
			v = &linkVerifier{t: t, root: root, open: make(map[int32]openedDir)}
		}
		if names, ok := v.handOver(f, uid, gid); ok {
			res.Files++
			res.Names += names
		} else {
			leave(f)
		}
	}

	fresh := t.fs.NewLinkTally()
	fresh.admit, fresh.limit = t.admit, t.limit
	*t = *fresh
	return res
}

// path returns a recorded name as it was given to Record.
func (t *LinkTally) path(n talliedName) string {
	if dir := t.dirs[n.dir]; dir != "." {
		return dir + "/" + n.name
	}
	return n.name
}

// linkVerifier is the state HandOver keeps between files.
type linkVerifier struct {
	t *LinkTally
	// root is the root of the walk.
	root int
	// open is the directories that are open, by their index in t.dirs. What a
	// path led to when it was opened may not be what it leads to now. That
	// does not matter, because a directory is told from another by what it
	// is, not by the path it was opened through.
	open map[int32]openedDir
	// read is how many directory entries have been listed.
	read  int
	found []foundName
}

type openedDir struct {
	fd int
	id fileID
}

// foundName is a recorded name that led to the file being looked at.
type foundName struct {
	dir  fileID
	idx  int32
	name string
}

func (v *linkVerifier) close() {
	v.closeDirs()
	_ = unix.Close(v.root)
}

func (v *linkVerifier) closeDirs() {
	for _, d := range v.open {
		_ = unix.Close(d.fd)
	}
	clear(v.open)
}

// dir returns the directory names recorded under idx are in, opening it if it
// is not open. The descriptor is only good until the next call.
func (v *linkVerifier) dir(idx int32) (openedDir, error) {
	if d, ok := v.open[idx]; ok {
		return d, nil
	}
	if len(v.open) >= tallyOpenDirs {
		v.closeDirs()
	}

	// One component at a time, and none of them a symbolic link. Whatever is
	// reached this way is a directory beneath the root of the walk, or was
	// when it was opened, and has been reached by its own name.
	path := v.t.dirs[idx]
	fd, owned := v.root, false
	for part := range strings.SplitSeq(path, "/") {
		// "." on its own is the root of the walk, which is opened again so
		// that every directory kept here is closed the same way.
		if part == "" || part == ".." || (part == "." && path != ".") {
			if owned {
				_ = unix.Close(fd)
			}
			return openedDir{}, unix.EINVAL
		}
		next, err := openNoFollow(fd, part, unix.O_DIRECTORY)
		if owned {
			_ = unix.Close(fd)
		}
		if err != nil {
			return openedDir{}, err
		}
		fd, owned = next, true
	}

	var st unix.Stat_t
	if err := ignoringEINTR(func() error { return unix.Fstat(fd, &st) }); err != nil {
		_ = unix.Close(fd)
		return openedDir{}, err
	}
	d := openedDir{fd: fd, id: idOf(&st)}
	v.open[idx] = d
	return d, nil
}

// openNoFollow opens name in dirfd for reading, without following it if it is
// a symbolic link.
func openNoFollow(dirfd int, name string, flag int) (int, error) {
	var fd int
	err := ignoringEINTR(func() (err error) {
		fd, err = unix.Openat(dirfd, name, flag|unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		return err
	})
	return fd, err
}

// lookup reports whether n leads to f at this moment, and from which
// directory.
func (v *linkVerifier) lookup(f *talliedFile, n talliedName) (openedDir, bool) {
	d, err := v.dir(n.dir)
	if err != nil {
		return openedDir{}, false
	}
	var st unix.Stat_t
	if err := ignoringEINTR(func() error {
		return unix.Fstatat(d.fd, n.name, &st, AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		return openedDir{}, false
	}
	return d, st.Mode&unix.S_IFMT == unix.S_IFREG && idOf(&st) == f.id
}

// handOver changes the owner of f if every name it has is one of those
// recorded for it, and returns how many names that was.
func (v *linkVerifier) handOver(f *talliedFile, uid, gid int) (int, bool) {
	fd, ok := v.openFile(f)
	if !ok {
		return 0, false
	}
	defer func() { _ = unix.Close(fd) }()

	names := v.count(f)

	// Read after the names were looked up, and as near to the change of owner
	// as it can be. A name added in between makes the count too high to match.
	var st unix.Stat_t
	if err := ignoringEINTR(func() error { return unix.Fstat(fd, &st) }); err != nil {
		return 0, false
	}
	if names == 0 || uint64(st.Nlink) != uint64(names) {
		return 0, false
	}
	if err := ignoringEINTR(func() error { return unix.Fchown(fd, uid, gid) }); err != nil {
		return 0, false
	}
	return names, true
}

// openFile opens f through the first of its names that still leads to it.
func (v *linkVerifier) openFile(f *talliedFile) (int, bool) {
	for _, n := range f.names {
		// Looked at before it is opened, so that nothing but a regular file is
		// opened unless it is put there between the two calls, and without
		// blocking in case it is: opening a pipe waits for its other end.
		// Nothing is read, so the descriptor can stay as it is.
		d, ok := v.lookup(f, n)
		if !ok {
			continue
		}
		fd, err := openNoFollow(d.fd, n.name, unix.O_NONBLOCK)
		if err != nil {
			continue
		}
		var st unix.Stat_t
		if err := ignoringEINTR(func() error { return unix.Fstat(fd, &st) }); err == nil &&
			st.Mode&unix.S_IFMT == unix.S_IFREG && idOf(&st) == f.id {
			return fd, true
		}
		_ = unix.Close(fd)
	}
	return 0, false
}

// count returns how many different names lead to f at this moment, out of
// those recorded for it.
func (v *linkVerifier) count(f *talliedFile) int {
	found := v.found[:0]
	for _, n := range f.names {
		if d, ok := v.lookup(f, n); ok {
			found = append(found, foundName{dir: d.id, idx: n.dir, name: n.name})
		}
	}
	v.found = found

	// The same name in the same directory is one name, however many paths the
	// directory was reached by.
	slices.SortFunc(found, func(a, b foundName) int {
		return cmp.Or(
			cmp.Compare(a.dir.dev, b.dir.dev),
			cmp.Compare(a.dir.ino, b.dir.ino),
			strings.Compare(a.name, b.name),
		)
	})
	found = slices.CompactFunc(found, func(a, b foundName) bool {
		return a.dir == b.dir && a.name == b.name
	})

	var names int
	for len(found) > 0 {
		n := 1
		for n < len(found) && found[n].dir == found[0].dir {
			n++
		}
		if n == 1 {
			names++
		} else {
			names += v.listed(found[:n])
		}
		found = found[n:]
	}
	return names
}

// listed returns how many of the names in group, which are all in one
// directory and all lead to the same file, the directory lists as they are
// spelled here.
//
// Two spellings that lead to one file are two names for it unless the volume
// takes them for the same name, as one that ignores case does with "a" and
// "A". What a volume takes for the same is its own business and differs
// between them, so it is not second-guessed: a directory holds each name once,
// in one spelling, and a spelling it does not list is another way of writing
// one it does.
func (v *linkVerifier) listed(group []foundName) int {
	d, err := v.dir(group[0].idx)
	if err != nil || d.id != group[0].dir {
		return 0
	}
	// Opened again, so that the listing starts at the beginning and the
	// descriptor that is closed when it ends is not the one being kept.
	fd, err := openNoFollow(d.fd, ".", unix.O_DIRECTORY)
	if err != nil {
		return 0
	}
	list := os.NewFile(uintptr(fd), ".")
	defer list.Close()

	want := make(map[string]bool, len(group))
	for _, n := range group {
		want[n.name] = false
	}
	var names int
	for {
		batch, err := list.Readdirnames(1024)
		for _, name := range batch {
			if seen, ok := want[name]; ok && !seen {
				want[name] = true
				names++
			}
		}
		v.read += len(batch)
		if err == io.EOF {
			return names
		}
		if err != nil || v.read > tallyListLimit {
			return 0
		}
	}
}
