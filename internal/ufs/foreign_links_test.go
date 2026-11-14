//go:build unix

package ufs_test

import (
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
