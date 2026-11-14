//go:build unix

package ufs_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pterodactyl/wings/internal/ufs"
)

// newForeignLinkRoot returns a guarded filesystem whose root holds a hard link
// to a file next to the root, and the path of that file. The guard is set up
// for an account other than the one the test runs as, which is what the files
// are to it: somebody else's.
func newForeignLinkRoot(t *testing.T) (*ufs.UnixFS, string, int) {
	t.Helper()

	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "host-file")
	require.NoError(t, os.Mkdir(root, 0o755))
	require.NoError(t, os.WriteFile(outside, []byte("not the server's"), 0o600))
	require.NoError(t, os.Link(outside, filepath.Join(root, "planted")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "own"), []byte("the server's"), 0o644))

	fs, err := ufs.NewUnixFS(root, false)
	require.NoError(t, err)
	account := os.Getuid() + 1000
	fs.GuardForeignLinks(account)
	return fs, outside, account
}

func ownerOf(t *testing.T, path string) int {
	t.Helper()
	st, err := os.Lstat(path)
	require.NoError(t, err)
	return int(st.Sys().(*syscall.Stat_t).Uid)
}

// A file with a second name outside the root is not read or written through
// the name inside it. The path of what was opened is inside the root, so the
// path check has nothing to object to.
func TestForeignLinkIsNotOpened(t *testing.T) {
	fs, outside, _ := newForeignLinkRoot(t)

	_, err := fs.Open("planted")
	require.ErrorIs(t, err, ufs.ErrForeignLink)

	_, err = fs.OpenFile("planted", ufs.O_RDWR, 0)
	require.ErrorIs(t, err, ufs.ErrForeignLink)

	_, err = fs.Touch("planted", ufs.O_RDWR, 0o644)
	require.ErrorIs(t, err, ufs.ErrForeignLink)

	got, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "not the server's", string(got))

	// A file with one name is the filesystem's own, whoever owns it.
	f, err := fs.Open("own")
	require.NoError(t, err)
	b, err := io.ReadAll(f)
	require.NoError(t, f.Close())
	require.NoError(t, err)
	assert.Equal(t, "the server's", string(b))

	// It is still listed, and can still be removed by name.
	entries, err := fs.ReadDir(".")
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	require.NoError(t, fs.Remove("planted"))
	_, err = os.Stat(outside)
	require.NoError(t, err)
}

// Once the file outside has lost its other name it is a file like any other.
func TestForeignLinkWithOneNameLeftIsOpened(t *testing.T) {
	fs, outside, _ := newForeignLinkRoot(t)
	require.NoError(t, os.Remove(outside))

	f, err := fs.Open("planted")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// The mode of a foreign link is not changed.
func TestForeignLinkIsNotChmodded(t *testing.T) {
	fs, outside, _ := newForeignLinkRoot(t)

	require.ErrorIs(t, fs.Chmod("planted", 0o666), ufs.ErrForeignLink)
	st, err := os.Stat(outside)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	require.NoError(t, fs.Chmod("own", 0o600))
	st, err = os.Stat(filepath.Join(fs.BasePath(), "own"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	require.NoError(t, fs.Mkdir("dir", 0o755))
	require.NoError(t, fs.Chmod("dir", 0o700))
	st, err = os.Stat(filepath.Join(fs.BasePath(), "dir"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), st.Mode().Perm())
}

// A foreign link is not handed to the account, which is what would let a
// server take over a file it only made a name for. Everything else is.
func TestForeignLinkIsNotChowned(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("changing a file's owner requires root")
	}
	fs, outside, account := newForeignLinkRoot(t)
	root := fs.BasePath()
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "dir", "nested"), nil, 0o644))

	require.ErrorIs(t, fs.Lchown("planted", account, account), ufs.ErrForeignLink)
	assert.Equal(t, 0, ownerOf(t, outside))

	var refused []string
	require.NoError(t, fs.WalkDirat(-1, root, func(dirfd int, name, relative string, _ ufs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := fs.Lchownat(dirfd, name, account, account); err != nil {
			if !assert.ErrorIs(t, err, ufs.ErrForeignLink) {
				return err
			}
			refused = append(refused, relative)
		}
		return nil
	}))
	assert.Equal(t, []string{"planted"}, refused)
	assert.Equal(t, 0, ownerOf(t, outside))
	for _, name := range []string{"own", "dir", "dir/nested"} {
		assert.Equal(t, account, ownerOf(t, filepath.Join(root, name)), name)
	}

	// A file the account owns already gives it nothing new, however many names
	// it has: a package manager's store linked into a project, for one.
	require.NoError(t, os.Link(filepath.Join(root, "own"), filepath.Join(root, "own-again")))
	f, err := fs.Open("own-again")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, fs.Lchown("own-again", account, account))
	require.NoError(t, fs.Chmod("own-again", 0o640))
}

// newGuardedRoot returns a guarded filesystem with nothing in it, the
// directory its root is in, and the account it is guarded for. As in
// newForeignLinkRoot, the account is not the one the test runs as, so every
// file the test makes is somebody else's.
func newGuardedRoot(t *testing.T) (*ufs.UnixFS, string, int) {
	t.Helper()

	base := t.TempDir()
	root := filepath.Join(base, "root")
	require.NoError(t, os.Mkdir(root, 0o755))

	fs, err := ufs.NewUnixFS(root, false)
	require.NoError(t, err)
	account := os.Getuid() + 1000
	fs.GuardForeignLinks(account)
	return fs, base, account
}

// writeLinked writes a file at the first path and gives it the others as
// further names, making the directories they are in.
func writeLinked(t *testing.T, paths ...string) {
	t.Helper()
	for i, p := range paths {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		if i == 0 {
			require.NoError(t, os.WriteFile(p, []byte("contents"), 0o600))
		} else {
			require.NoError(t, os.Link(paths[0], p))
		}
	}
}

// chownTree does what starting a server does to its files: it walks the root,
// gives everything to the account, and records the names the guard refuses.
// visited, if it is not nil, is called once each name has been dealt with,
// which is where a test changes the tree under the walk. What is returned
// hands over what was recorded.
//
// Only root can give a file away. Anyone else asks for no change of owner,
// which goes through every check and then changes nothing, so that what was
// decided can be tested without root: it is in the result. That the owner
// really changes is tested where the test is root.
func chownTree(t *testing.T, fs *ufs.UnixFS, account int, visited func(relative string)) func() ufs.LinkTallyResult {
	t.Helper()

	uid, gid := -1, -1
	if os.Getuid() == 0 {
		uid, gid = account, account
	}
	rootfd, root, closeFd, err := fs.SafePath(".")
	t.Cleanup(closeFd)
	require.NoError(t, err)

	tally := fs.NewLinkTally()
	require.NoError(t, fs.WalkDirat(rootfd, root, func(dirfd int, name, relative string, _ ufs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := fs.Lchownat(dirfd, name, uid, gid); err != nil {
			if !errors.Is(err, ufs.ErrForeignLink) {
				return err
			}
			tally.Record(dirfd, name, relative)
		}
		if visited != nil {
			visited(relative)
		}
		return nil
	}))
	return func() ufs.LinkTallyResult {
		return tally.HandOver(rootfd, root, uid, gid)
	}
}

// assertOwner checks who a file belongs to, where the test could have changed
// it.
func assertOwner(t *testing.T, want int, path string) {
	t.Helper()
	if os.Getuid() == 0 {
		assert.Equal(t, want, ownerOf(t, path), path)
	}
}

// assertLeft checks that nothing was handed over and that count names were
// left, the first of them one of first. A walk meets names in the order the
// directory holds them, which is not one a test can rely on.
func assertLeft(t *testing.T, res ufs.LinkTallyResult, count int, first ...string) {
	t.Helper()
	assert.Contains(t, first, res.First)
	res.First = ""
	assert.Equal(t, ufs.LinkTallyResult{Left: count}, res)
}

// plantedAheadOfTheWalk gives the file at outside one name beneath root, in
// one of the directories "a" and "b", and returns something to call as the
// walk goes that keeps moving that name to where the walk has not been yet, so
// that it is met in both. It reports which directory the name was met in
// first; it is in the other one when the walk is over.
func plantedAheadOfTheWalk(t *testing.T, root, outside string) (visited func(relative string), metFirst func() string) {
	t.Helper()
	writeLinked(t, outside, filepath.Join(root, "a", "planted"))
	require.NoError(t, os.Mkdir(filepath.Join(root, "b"), 0o755))

	var first, second string
	move := func(from, to string) {
		require.NoError(t, os.Rename(filepath.Join(root, from, "planted"), filepath.Join(root, to, "planted")))
	}
	return func(relative string) {
		switch {
		case first == "" && relative == "a":
			first, second = "a", "b"
		case first == "" && relative == "b":
			// The walk is about to read "b", and has not been to "a".
			first, second = "b", "a"
			move("a", "b")
		case first != "" && relative == first+"/planted":
			move(first, second)
		}
	}, func() string { return first }
}

// A file with several names, all of them beneath the root, has no name
// anywhere else and is the account's like any other file there: a package
// manager's store linked into a project, or the many names of one program in
// one directory.
func TestHardLinksInsideTheRootAreHandedOver(t *testing.T) {
	fs, _, account := newGuardedRoot(t)
	root := fs.BasePath()
	store := filepath.Join(root, "store", "ab", "cdef")
	project := filepath.Join(root, "project", "node_modules", "pkg", "index.js")
	writeLinked(t, store, project)
	tools := []string{
		filepath.Join(root, "bin", "tool"),
		filepath.Join(root, "bin", "tool-add"),
		filepath.Join(root, "bin", "tool-remove"),
	}
	writeLinked(t, tools...)
	writeLinked(t, filepath.Join(root, "top"), filepath.Join(root, "top-again"), filepath.Join(root, "deep", "er", "top"))

	res := chownTree(t, fs, account, nil)()
	assert.Equal(t, ufs.LinkTallyResult{Files: 3, Names: 8}, res)

	for _, p := range append([]string{store, project}, tools...) {
		assertOwner(t, account, p)
	}
	if os.Getuid() != 0 {
		return
	}
	// They are the account's now, so nothing is refused any more.
	f, err := fs.Open("project/node_modules/pkg/index.js")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, ufs.LinkTallyResult{}, chownTree(t, fs, account, nil)())
}

// One name beneath the root and one outside of it: the file is somebody
// else's, and stays theirs.
func TestHardLinkWithANameOutsideTheRootIsLeft(t *testing.T) {
	fs, outside, account := newForeignLinkRoot(t)

	res := chownTree(t, fs, account, nil)()
	assert.Equal(t, ufs.LinkTallyResult{Left: 1, First: "planted"}, res)
	assertOwner(t, os.Getuid(), outside)
	assertOwner(t, account, filepath.Join(fs.BasePath(), "own"))
}

// Two names beneath the root do not make up for a third outside of it.
func TestHardLinkWithOneOfThreeNamesOutsideTheRootIsLeft(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	outside := filepath.Join(base, "host-file")
	writeLinked(t, outside, filepath.Join(root, "a", "one"), filepath.Join(root, "b", "two"))

	assertLeft(t, chownTree(t, fs, account, nil)(), 2, "a/one", "b/two")
	assertOwner(t, os.Getuid(), outside)
}

// A name that is moved ahead of the walk is met twice. Counted as it is met,
// the one name beneath the root would come to two, which is how many names
// the file has, and the file outside would be handed over.
func TestHardLinkMovedAheadOfTheWalkIsLeft(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	outside := filepath.Join(base, "host-file")
	visited, metFirst := plantedAheadOfTheWalk(t, root, outside)

	res := chownTree(t, fs, account, visited)()
	// Two names were recorded, which is what makes this the case it is meant
	// to be, and neither is handed over.
	assertLeft(t, res, 2, metFirst()+"/planted")
	assertOwner(t, os.Getuid(), outside)
}

// The same, with the directory the name was first met in then replaced by a
// symbolic link to the one it was moved to. Both recorded paths now lead to
// the file, at the same time and for as long as anyone cares to look, and they
// are still one name.
func TestHardLinkReachedThroughASymlinkedDirectoryIsLeft(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	outside := filepath.Join(base, "host-file")
	visited, metFirst := plantedAheadOfTheWalk(t, root, outside)

	handOver := chownTree(t, fs, account, visited)
	emptied := metFirst()
	holding := map[string]string{"a": "b", "b": "a"}[emptied]
	require.NoError(t, os.Remove(filepath.Join(root, emptied)))
	require.NoError(t, os.Symlink(holding, filepath.Join(root, emptied)))
	for _, dir := range []string{"a", "b"} {
		st, err := os.Stat(filepath.Join(root, dir, "planted"))
		require.NoError(t, err, dir)
		require.Equal(t, uint64(2), uint64(st.Sys().(*syscall.Stat_t).Nlink))
	}

	assertLeft(t, handOver(), 2, emptied+"/planted")
	assertOwner(t, os.Getuid(), outside)
}

// The same again, with the directory replaced by a symbolic link to the one
// the file's other name is in, outside of the root. The two recorded paths
// lead to the file by two names that really are different, and one of them is
// the name that must not be counted.
func TestHardLinkReachedThroughASymlinkOutOfTheRootIsLeft(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	outside := filepath.Join(base, "elsewhere", "planted")
	visited, metFirst := plantedAheadOfTheWalk(t, root, outside)

	handOver := chownTree(t, fs, account, visited)
	emptied := metFirst()
	require.NoError(t, os.Remove(filepath.Join(root, emptied)))
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(root, emptied)))
	for _, dir := range []string{"a", "b"} {
		st, err := os.Stat(filepath.Join(root, dir, "planted"))
		require.NoError(t, err, dir)
		require.Equal(t, uint64(2), uint64(st.Sys().(*syscall.Stat_t).Nlink))
	}

	assertLeft(t, handOver(), 2, emptied+"/planted")
	assertOwner(t, os.Getuid(), outside)
}

// On a volume that ignores case a name can be written more than one way, and
// every way of writing it leads to the file. They are one name.
func TestHardLinkSpelledTwoWaysIsOneName(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	require.NoError(t, os.WriteFile(filepath.Join(root, "probe"), nil, 0o644))
	if _, err := os.Lstat(filepath.Join(root, "PROBE")); err != nil {
		t.Skip("the volume tells names that differ in case apart")
	}
	outside := filepath.Join(base, "host-file")
	writeLinked(t, outside, filepath.Join(root, "dir", "planted"))

	uid, gid := -1, -1
	if os.Getuid() == 0 {
		uid, gid = account, account
	}
	rootfd, rootName, closeRoot, err := fs.SafePath(".")
	defer closeRoot()
	require.NoError(t, err)
	dirfd, _, closeDir, err := fs.SafePath("dir/planted")
	defer closeDir()
	require.NoError(t, err)

	tally := fs.NewLinkTally()
	tally.Record(dirfd, "planted", "dir/planted")
	tally.Record(dirfd, "PLANTED", "dir/PLANTED")
	assertLeft(t, tally.HandOver(rootfd, rootName, uid, gid), 2, "dir/planted")
	assertOwner(t, os.Getuid(), outside)
}

// What counts is the names a file has when it is handed over, not when the
// walk went past. A name added outside of the root in between is one more
// than can be accounted for.
func TestHardLinkGivenANameOutsideTheRootAfterTheWalkIsLeft(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	writeLinked(t, filepath.Join(root, "a", "file"), filepath.Join(root, "b", "file"))

	handOver := chownTree(t, fs, account, nil)
	outside := filepath.Join(base, "host-file")
	require.NoError(t, os.Link(filepath.Join(root, "a", "file"), outside))

	assertLeft(t, handOver(), 2, "a/file", "b/file")
	assertOwner(t, os.Getuid(), outside)
}

// A name that has gone by then is not needed, as long as the names that are
// left are all there are.
func TestHardLinkThatLostANameAfterTheWalkIsHandedOver(t *testing.T) {
	fs, _, account := newGuardedRoot(t)
	root := fs.BasePath()
	writeLinked(t, filepath.Join(root, "a", "file"), filepath.Join(root, "b", "file"), filepath.Join(root, "c", "file"))

	handOver := chownTree(t, fs, account, nil)
	require.NoError(t, os.Remove(filepath.Join(root, "a", "file")))

	assert.Equal(t, ufs.LinkTallyResult{Files: 1, Names: 2}, handOver())
	assertOwner(t, account, filepath.Join(root, "b", "file"))
}

// A recorded name that leads to some other file by the time it is looked at
// again is not a name of the file it was recorded for, and the file it leads
// to now is not touched on the strength of it.
func TestHardLinkReplacedAfterTheWalkIsNotMistakenForTheFile(t *testing.T) {
	fs, base, account := newGuardedRoot(t)
	root := fs.BasePath()
	writeLinked(t, filepath.Join(root, "a", "file"), filepath.Join(root, "b", "file"))
	outside := filepath.Join(base, "host-file")
	require.NoError(t, os.WriteFile(outside, []byte("not the server's"), 0o600))

	handOver := chownTree(t, fs, account, nil)
	require.NoError(t, os.Remove(filepath.Join(root, "a", "file")))
	require.NoError(t, os.Link(outside, filepath.Join(root, "a", "file")))

	// The file that was recorded has one name left, and that is handed over.
	assert.Equal(t, ufs.LinkTallyResult{Files: 1, Names: 1}, handOver())
	assertOwner(t, account, filepath.Join(root, "b", "file"))
	assertOwner(t, os.Getuid(), outside)
}

// Only regular files are handed over this way. Anything else with other names
// is left as the guard left it.
func TestHardLinkedFifoIsLeft(t *testing.T) {
	fs, _, account := newGuardedRoot(t)
	root := fs.BasePath()
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600))
	require.NoError(t, os.Link(filepath.Join(root, "pipe"), filepath.Join(root, "pipe-again")))

	assertLeft(t, chownTree(t, fs, account, nil)(), 2, "pipe", "pipe-again")
	assertOwner(t, os.Getuid(), filepath.Join(root, "pipe"))
}
