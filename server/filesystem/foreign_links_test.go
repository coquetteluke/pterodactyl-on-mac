package filesystem

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pterodactyl/wings/internal/ufs"
)

// newForeignLinkFilesystem returns a filesystem that guards against foreign
// links, as one for a server with its own account on darwin does, holding a
// file of its own and a hard link to a file next to its root.
func newForeignLinkFilesystem(t *testing.T) (*Filesystem, string) {
	t.Helper()
	fsys, rfs := NewFs()
	t.Cleanup(func() { _ = os.RemoveAll(rfs.root) })

	// Any account but the one the test runs as, which owns the files.
	fsys.owner = Owner{UID: os.Getuid() + 1000, GID: os.Getgid() + 1000}
	fsys.unixFS.GuardForeignLinks(fsys.owner.UID)

	outside := filepath.Join(rfs.root, "host-file")
	require.NoError(t, os.WriteFile(outside, []byte("not the server's"), 0o600))
	require.NoError(t, os.Link(outside, filepath.Join(fsys.Path(), "planted")))
	require.NoError(t, os.WriteFile(filepath.Join(fsys.Path(), "own.txt"), []byte("the server's"), 0o644))
	return fsys, outside
}

// A hard link to a file outside the server is listed, so that it can be seen
// and removed, without the file being opened to work out what it holds.
func TestListDirectoryListsForeignLinkWithoutReadingIt(t *testing.T) {
	fsys, _ := newForeignLinkFilesystem(t)

	entries, err := fsys.ListDirectory("/")
	require.NoError(t, err)
	names := make(map[string]string, len(entries))
	for _, e := range entries {
		names[e.Name()] = e.Mimetype
	}
	assert.Equal(t, map[string]string{
		"own.txt": "text/plain; charset=utf-8",
		"planted": "application/octet-stream",
	}, names)
}

// The file is left out of an archive and the rest of the archive is written.
func TestArchiveLeavesOutForeignLink(t *testing.T) {
	fsys, _ := newForeignLinkFilesystem(t)

	entries := archiveEntries(t, fsys)
	assert.Equal(t, []string{"own.txt"}, archiveEntryNames(entries))
}

// It is not read or written through the server's name for it.
func TestForeignLinkIsNotReadOrWritten(t *testing.T) {
	fsys, outside := newForeignLinkFilesystem(t)

	_, _, err := fsys.File("planted")
	require.ErrorIs(t, err, ufs.ErrForeignLink)

	err = fsys.Write("planted", nil, 0, 0o644)
	require.ErrorIs(t, err, ufs.ErrForeignLink)

	got, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "not the server's", string(got))
}

// Handing a server its files does not hand it the file outside, and does not
// stop at it either: a server that will not start is the one outcome the link
// should not be able to cause.
func TestChownLeavesForeignLinkWithItsOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("changing a file's owner requires root")
	}
	fsys, outside := newForeignLinkFilesystem(t)
	fsys.isTest = false
	t.Cleanup(func() { fsys.isTest = true })

	require.NoError(t, fsys.Chown("/"))
	require.NoError(t, fsys.Chown("planted"))

	assert.Equal(t, 0, uidOf(t, outside))
	assert.Equal(t, fsys.owner.UID, uidOf(t, filepath.Join(fsys.Path(), "own.txt")))
	assert.Equal(t, fsys.owner.UID, uidOf(t, fsys.Path()))
}

// rootOnly returns the filesystem ready to change owners for real, which only
// root can do.
func rootOnly(t *testing.T, fsys *Filesystem) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("changing a file's owner requires root")
	}
	fsys.isTest = false
	t.Cleanup(func() { fsys.isTest = true })
}

// link gives the file at the first path the others as further names, making
// the file and the directories as it goes.
func link(t *testing.T, paths ...string) {
	t.Helper()
	for i, p := range paths {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		if i == 0 {
			require.NoError(t, os.WriteFile(p, []byte("linked"), 0o600))
		} else {
			require.NoError(t, os.Link(paths[0], p))
		}
	}
}

func uidOf(t *testing.T, path string) int {
	t.Helper()
	st, err := os.Lstat(path)
	require.NoError(t, err)
	return int(st.Sys().(*syscall.Stat_t).Uid)
}

// A server's own hard-linked files look like links to files from outside the
// first time it is given an account: several names, and an owner that is not
// the account. Every one of their names is in the server's directory, and they
// are handed over with the rest. Until this was so they were left out of every
// backup from then on.
func TestChownHandsOverHardLinksWithNoNameOutside(t *testing.T) {
	fsys, outside := newForeignLinkFilesystem(t)
	rootOnly(t, fsys)
	root := fsys.Path()
	link(t, filepath.Join(root, "store", "blob"), filepath.Join(root, "project", "blob"))
	link(t, filepath.Join(root, "bin", "tool"), filepath.Join(root, "bin", "tool-add"))

	entries := archiveEntries(t, fsys)
	assert.Equal(t, []string{"own.txt"}, archiveEntryNames(entries))

	require.NoError(t, fsys.Chown("/"))

	for _, name := range []string{"store/blob", "project/blob", "bin/tool", "bin/tool-add", "own.txt"} {
		assert.Equal(t, fsys.owner.UID, uidOf(t, filepath.Join(root, name)), name)
	}
	// The file that is somebody else's is still theirs, in the same walk.
	assert.Equal(t, 0, uidOf(t, outside))

	entries = archiveEntries(t, fsys)
	assert.Equal(t,
		[]string{"bin/tool", "bin/tool-add", "own.txt", "project/blob", "store/blob"},
		archiveEntryNames(entries))

	f, _, err := fsys.File("project/blob")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

// Two names in the server's directory and a third outside of it: the file is
// reachable from somewhere the server is not, and is left with its owner.
func TestChownLeavesHardLinkWithOneOfThreeNamesOutside(t *testing.T) {
	fsys, _ := newForeignLinkFilesystem(t)
	rootOnly(t, fsys)
	root := fsys.Path()
	shared := filepath.Join(filepath.Dir(root), "shared-file")
	link(t, shared, filepath.Join(root, "a", "one"), filepath.Join(root, "b", "two"))

	require.NoError(t, fsys.Chown("/"))

	assert.Equal(t, 0, uidOf(t, shared))
	assert.Equal(t, fsys.owner.UID, uidOf(t, filepath.Join(root, "a")))

	entries := archiveEntries(t, fsys)
	assert.Equal(t, []string{"own.txt"}, archiveEntryNames(entries))
	_, _, err := fsys.File("a/one")
	require.ErrorIs(t, err, ufs.ErrForeignLink)
}

// Handing over part of a server's directory accounts for the names in that
// part. A file with a name elsewhere in the server's directory is left until
// the whole of it is walked, which it is every time the server starts.
func TestChownOfASubdirectoryLeavesHardLinksThatReachOutOfIt(t *testing.T) {
	fsys, _ := newForeignLinkFilesystem(t)
	rootOnly(t, fsys)
	root := fsys.Path()
	link(t, filepath.Join(root, "store", "blob"), filepath.Join(root, "project", "blob"))
	link(t, filepath.Join(root, "project", "inner"), filepath.Join(root, "project", "deeper", "inner"))

	require.NoError(t, fsys.Chown("project"))
	assert.Equal(t, 0, uidOf(t, filepath.Join(root, "project", "blob")))
	assert.Equal(t, fsys.owner.UID, uidOf(t, filepath.Join(root, "project", "inner")))

	require.NoError(t, fsys.Chown("/"))
	assert.Equal(t, fsys.owner.UID, uidOf(t, filepath.Join(root, "store", "blob")))
}
