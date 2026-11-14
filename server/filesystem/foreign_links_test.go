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

	owner := func(path string) int {
		st, err := os.Lstat(path)
		require.NoError(t, err)
		return int(st.Sys().(*syscall.Stat_t).Uid)
	}
	assert.Equal(t, 0, owner(outside))
	assert.Equal(t, fsys.owner.UID, owner(filepath.Join(fsys.Path(), "own.txt")))
	assert.Equal(t, fsys.owner.UID, owner(fsys.Path()))
}
