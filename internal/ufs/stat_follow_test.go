//go:build unix

package ufs_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/internal/ufs"
)

// Describing a link never opens what it points at when that is outside the
// root. A FIFO shows whether it was opened: a writer waiting on one is
// released the moment anything opens it for reading.
func TestStatDoesNotOpenLinkTargetsOutsideRoot(t *testing.T) {
	for _, mode := range []struct {
		name    string
		openat2 bool
	}{{"openat2", true}, {"openat", false}} {
		t.Run(mode.name, func(t *testing.T) {
			fs, outside := newLinkedRoot(t, mode.openat2)
			fifo := filepath.Join(outside, "fifo")
			require.NoError(t, unix.Mkfifo(fifo, 0o600))
			require.NoError(t, os.Symlink(fifo, filepath.Join(fs.BasePath(), "fifolink")))

			opened := make(chan struct{})
			go func() {
				// Blocks until the FIFO has a reader.
				if fd, err := unix.Open(fifo, unix.O_WRONLY, 0); err == nil {
					_ = unix.Close(fd)
				}
				close(opened)
			}()
			t.Cleanup(func() {
				// Let the writer go, whatever happened above.
				if fd, err := unix.Open(fifo, unix.O_RDONLY|unix.O_NONBLOCK, 0); err == nil {
					<-opened
					_ = unix.Close(fd)
				}
			})
			// Give the writer time to be waiting in open.
			time.Sleep(100 * time.Millisecond)

			_, err := fs.Stat("fifolink")
			require.ErrorIs(t, err, ufs.ErrBadPathResolution)

			select {
			case <-opened:
				t.Fatal("the FIFO outside the root was opened in order to describe a link to it")
			case <-time.After(250 * time.Millisecond):
			}
		})
	}
}

// Stat follows a chain of links, through linked directories and upwards, for
// as long as every step stays inside the root.
func TestStatFollowsChainsOfLinksInsideRoot(t *testing.T) {
	for _, mode := range []struct {
		name    string
		openat2 bool
	}{{"openat2", true}, {"openat", false}} {
		t.Run(mode.name, func(t *testing.T) {
			fs, _ := newLinkedRoot(t, mode.openat2)
			root := fs.BasePath()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "a", "b", "data.txt"), []byte("12345"), 0o644))
			require.NoError(t, unix.Mkfifo(filepath.Join(root, "a", "pipe"), 0o600))
			// dir -> a/b, then a file reached through that linked directory.
			require.NoError(t, os.Symlink("a/b", filepath.Join(root, "dir")))
			require.NoError(t, os.Symlink("dir/data.txt", filepath.Join(root, "one")))
			// A link to a link, written as an absolute path and going up a level.
			require.NoError(t, os.Symlink(filepath.Join(root, "one"), filepath.Join(root, "a", "two")))
			require.NoError(t, os.Symlink("../a/two", filepath.Join(root, "a", "three")))
			require.NoError(t, os.Symlink("a/pipe", filepath.Join(root, "pipelink")))
			require.NoError(t, os.Symlink("nowhere", filepath.Join(root, "dangling")))
			require.NoError(t, os.Symlink("loop-b", filepath.Join(root, "loop-a")))
			require.NoError(t, os.Symlink("loop-a", filepath.Join(root, "loop-b")))
			// Leaves the root on the way to a name that is back inside it.
			require.NoError(t, os.Symlink("../"+filepath.Base(root)+"/a/b/data.txt", filepath.Join(root, "roundabout")))

			names := []string{"one"}
			if !mode.openat2 {
				// openat2 refuses a link written as an absolute path, or one
				// that steps out of the root on its way, without looking at
				// where it ends up. Without it, where it ends up is what counts.
				names = append(names, "a/two", "a/three", "roundabout")
			}
			for _, name := range names {
				st, err := fs.Stat(name)
				require.NoError(t, err, name)
				require.True(t, st.Mode().IsRegular(), name)
				require.Equal(t, int64(5), st.Size(), name)
				require.Equal(t, filepath.Base(name), st.Name(), name)
			}

			st, err := fs.Stat("dir")
			require.NoError(t, err)
			require.True(t, st.IsDir())

			// A FIFO inside the root is described, not opened: this would hang.
			st, err = fs.Stat("pipelink")
			require.NoError(t, err)
			require.NotZero(t, st.Mode()&ufs.ModeNamedPipe)

			_, err = fs.Stat("dangling")
			require.ErrorIs(t, err, ufs.ErrNotExist)

			_, err = fs.Stat("loop-a")
			require.Error(t, err)
		})
	}
}
