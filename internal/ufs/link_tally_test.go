// SPDX-License-Identifier: MIT

//go:build unix

package ufs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTallyRoot returns a guarded filesystem with nothing in it and the
// directory its root is in. The account it is guarded for is not the one the
// test runs as, so every file the test makes is somebody else's.
func newTallyRoot(t *testing.T) (*UnixFS, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	require.NoError(t, os.Mkdir(root, 0o755))
	fs, err := NewUnixFS(root, false)
	require.NoError(t, err)
	fs.GuardForeignLinks(os.Getuid() + 1000)
	return fs, base
}

// record records the name at the path beneath the root, as a walk that had
// been refused it would.
func record(t *testing.T, fs *UnixFS, tally *LinkTally, relative string) {
	t.Helper()
	dirfd, name, closeFd, err := fs.SafePath(relative)
	defer closeFd()
	require.NoError(t, err)
	tally.Record(dirfd, name, relative)
}

// A directory that is moved while files are being handed over can be open
// under the path it had and be opened again under the path it has. A name in
// it is recorded under both if it was moved during the walk as well. That is
// one name, found twice.
func TestLinkTallyCountsANameInAMovedDirectoryOnce(t *testing.T) {
	fs, base := newTallyRoot(t)
	root := fs.BasePath()
	outside := filepath.Join(base, "host-file")
	require.NoError(t, os.WriteFile(outside, []byte("not the server's"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "a"), 0o755))
	require.NoError(t, os.Link(outside, filepath.Join(root, "a", "planted")))

	rootfd, err := fs.openat(AT_EMPTY_PATH, root, O_DIRECTORY|O_RDONLY, 0)
	require.NoError(t, err)
	tally := fs.NewLinkTally()
	v := &linkVerifier{t: tally, root: rootfd, open: make(map[int32]openedDir)}
	defer v.close()

	record(t, fs, tally, "a/planted")
	held, err := v.dir(0)
	require.NoError(t, err)

	require.NoError(t, os.Rename(filepath.Join(root, "a"), filepath.Join(root, "b")))
	record(t, fs, tally, "b/planted")
	moved, err := v.dir(1)
	require.NoError(t, err)
	require.NotEqual(t, held.fd, moved.fd)

	require.Len(t, tally.order, 1)
	f := tally.order[0]
	require.Len(t, f.names, 2)
	require.Equal(t, uint64(2), f.nlink)
	assert.Equal(t, 1, v.count(f))

	_, ok := v.handOver(f, -1, -1)
	assert.False(t, ok)
}

// The names of a tree are only kept up to a point, since a server decides how
// many there are. Past it no further file is taken on, and the files already
// taken on are still finished, so that the next walk has fewer to deal with.
func TestLinkTallyStopsTakingOnFiles(t *testing.T) {
	fs, _ := newTallyRoot(t)
	root := fs.BasePath()
	for _, dir := range []string{"store", "project"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0o755))
	}
	for _, name := range []string{"one", "two", "three"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "store", name), nil, 0o600))
		require.NoError(t, os.Link(filepath.Join(root, "store", name), filepath.Join(root, "project", name)))
	}

	tally := fs.NewLinkTally()
	// Room to take on two files, and then to finish them.
	tally.admit = 2*(tallyFileCost+tallyNameCost+len("one")) + tallyDirCost + len("store")
	tally.limit = 2 * tally.admit
	for _, relative := range []string{
		"store/one", "store/two", "store/three",
		"project/three", "project/two", "project/one",
	} {
		record(t, fs, tally, relative)
	}
	assert.LessOrEqual(t, tally.size, tally.limit)

	rootfd, name, closeFd, err := fs.SafePath(".")
	defer closeFd()
	require.NoError(t, err)
	res := tally.HandOver(rootfd, name, -1, -1)
	assert.Equal(t, LinkTallyResult{Files: 2, Names: 4, Left: 2, First: "store/three", Unseen: 2}, res)

}

// Nothing is kept at all past the second limit. A file that had a name turned
// away has fewer names recorded than it has links, and is left.
func TestLinkTallyKeepsNothingPastItsLimit(t *testing.T) {
	fs, _ := newTallyRoot(t)
	root := fs.BasePath()
	paths := []string{"file", "file-two", "file-three"}
	require.NoError(t, os.WriteFile(filepath.Join(root, paths[0]), nil, 0o600))
	for _, p := range paths[1:] {
		require.NoError(t, os.Link(filepath.Join(root, paths[0]), filepath.Join(root, p)))
	}

	tally := fs.NewLinkTally()
	tally.admit = tallyFileCost + tallyDirCost + len(".") + tallyNameCost + len(paths[0])
	tally.limit = tally.admit + tallyNameCost + len(paths[1])
	for _, p := range paths {
		record(t, fs, tally, p)
	}
	assert.LessOrEqual(t, tally.size, tally.limit)

	require.Len(t, tally.order, 1)
	require.Len(t, tally.order[0].names, 2)

	rootfd, name, closeFd, err := fs.SafePath(".")
	defer closeFd()
	require.NoError(t, err)
	res := tally.HandOver(rootfd, name, -1, -1)
	assert.Equal(t, LinkTallyResult{Left: 3, First: "file", Unseen: 1}, res)
}
