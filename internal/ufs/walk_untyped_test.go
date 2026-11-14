//go:build unix

package ufs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/internal/ufs"
)

// listedDir is the directory the tests below list, and wantListing what it
// holds.
const listedDir = "srv"

var wantListing = map[string]ufs.FileMode{
	"file": 0,
	"dir":  ufs.ModeDir,
	"link": ufs.ModeSymlink,
}

// newListedRoot returns a root whose listedDir holds a file, a directory and a
// symlink, and moves the test into a working directory that answers a lookup
// by path wrongly. From there both "srv/<entry>" and "./<entry>" name another
// kind of file for "file" and "dir", and nothing at all for "link".
func newListedRoot(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "root")
	dir := filepath.Join(root, listedDir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "dir"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file"), []byte("file"), 0o644))
	require.NoError(t, os.Symlink("file", filepath.Join(dir, "link")))

	decoy := t.TempDir()
	for _, parent := range []string{decoy, filepath.Join(decoy, listedDir)} {
		require.NoError(t, os.MkdirAll(filepath.Join(parent, "file"), 0o755))
		require.NoError(t, os.Symlink("file", filepath.Join(parent, "dir")))
	}
	t.Chdir(decoy)

	return root
}

// A directory is listed in full, and every entry is given its own type,
// whatever its name leads to from the working directory.
//
// Where the filesystem fills in d_type (APFS, HFS+, ext4) the type comes with
// the entry and nothing is looked up. Run with TMPDIR on a volume that leaves
// it unset, this is the stat that stands in for it.
func TestReadDirIgnoresWorkingDirectory(t *testing.T) {
	fs, err := ufs.NewUnixFS(newListedRoot(t), true)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fs.Close() })

	typesOf := func(entries []ufs.DirEntry) map[string]ufs.FileMode {
		types := make(map[string]ufs.FileMode, len(entries))
		for _, e := range entries {
			types[e.Name()] = e.Type()
		}
		return types
	}

	// ReadDir reads the directory under its own name and ReadDirMap under ".",
	// the two names readDir is ever given.
	entries, err := fs.ReadDir(listedDir)
	require.NoError(t, err)
	require.Equal(t, wantListing, typesOf(entries))

	entries, err = ufs.ReadDirMap(fs, listedDir, func(e ufs.DirEntry) (ufs.DirEntry, error) {
		return e, nil
	})
	require.NoError(t, err)
	require.Equal(t, wantListing, typesOf(entries))
}

// darwin's readDir hands a bare descriptor to os.File.ReadDir, under a name
// that does not lead back to the directory. An entry the filesystem leaves
// untyped (DT_UNKNOWN: some network and FUSE volumes, never APFS or HFS+) is
// one the os package has to stat, and the listing is right only if it does so
// through the descriptor. It has since Go 1.25.8 and 1.26.1. Before that it
// called lstat on name/entry, which is looked up in the working directory.
//
// APFS cannot be made to return an untyped entry, so this goes through
// Readdir, which describes every entry with the call ReadDir makes for an
// untyped one.
func TestOSDescribesEntriesThroughDescriptor(t *testing.T) {
	dir := filepath.Join(newListedRoot(t), listedDir)

	for _, name := range []string{listedDir, "."} {
		fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		require.NoError(t, err)
		f := os.NewFile(uintptr(fd), name)
		infos, err := f.Readdir(-1)
		require.NoError(t, f.Close())
		require.NoError(t, err)

		types := make(map[string]ufs.FileMode, len(infos))
		for _, info := range infos {
			types[info.Name()] = info.Mode().Type()
		}
		require.Equal(t, wantListing, types,
			"os.File looked the entries of a directory wrapped as %q up by path: "+
				"readDir in walk_darwin.go cannot list a volume that leaves d_type unset", name)
	}
}
