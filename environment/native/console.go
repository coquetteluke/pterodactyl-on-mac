package native

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"

	"emperror.dev/errors"
	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/remote"
)

// ErrNotAttached is returned when a command is sent to a server whose console
// is not currently wired up.
var ErrNotAttached = errors.Sentinel("environment/native: not attached to instance")

// consolePollInterval is how often the console log is checked for new output.
//
// There is no readable equivalent of `docker attach` for a detached process,
// so the log file is tailed. 50ms keeps console latency below what anyone
// notices while costing a single fstat per tick on an idle server.
const consolePollInterval = 50 * time.Millisecond

// maxConsoleLineLength bounds a single console line so that a server spewing
// output with no newline cannot grow the buffer without limit.
const maxConsoleLineLength = 64 * 1024

// Create prepares the plumbing a server needs before it can be started: the
// runtime directory and the stdin FIFO.
//
// Unlike the Docker environment there is no container object to build here, so
// this is cheap and safe to call repeatedly.
func (e *Environment) Create() error {
	if err := os.MkdirAll(e.runtimeDir(), 0o700); err != nil {
		return errors.Wrap(err, "environment/native: failed to create runtime directory")
	}

	// A FIFO is used for stdin rather than an anonymous pipe so that the
	// console survives wings restarting: the running server keeps its read end
	// open, and a new wings process can reopen the write end and carry on
	// sending commands.
	fifo := e.fifoPath()
	st, err := os.Stat(fifo)
	switch {
	case err == nil && st.Mode()&os.ModeNamedPipe != 0:
		// Already a FIFO, nothing to do.
	case err == nil:
		// Something is there but it is not a FIFO. Replace it.
		if err := os.Remove(fifo); err != nil {
			return errors.Wrap(err, "environment/native: failed to replace stdin path")
		}
		fallthrough
	default:
		if err := unix.Mkfifo(fifo, 0o600); err != nil && !os.IsExist(err) {
			return errors.Wrap(err, "environment/native: failed to create stdin fifo")
		}
	}

	// Ensure the server's data directory exists; the Docker environment gets
	// this for free when it creates the bind mount.
	dir, err := e.workingDir()
	if err != nil {
		return err
	}

	// With a dedicated account, the directory itself has to deny traversal to
	// everyone else. Chowning the contents is not enough on its own: files
	// created with the usual 0644 stay world-readable, so another server could
	// still read them by walking in. Denying execute on the directory is what
	// actually stops that.
	e.mu.RLock()
	account := e.meta.Account
	e.mu.RUnlock()

	mode := os.FileMode(0o755)
	if account != nil {
		mode = 0o750
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return errors.Wrap(err, "environment/native: failed to create server directory")
	}
	if account != nil {
		// MkdirAll leaves an existing directory's mode alone, so tighten it
		// explicitly for servers that predate the account being enabled.
		if err := os.Chmod(dir, mode); err != nil {
			return errors.Wrap(err, "environment/native: failed to restrict server directory")
		}
		if err := os.Chown(dir, account.UID, account.GID); err != nil {
			return errors.Wrap(err, "environment/native: failed to hand the server directory to its account")
		}
	}

	return nil
}

// Destroy stops the server and removes the runtime state wings keeps for it.
//
// The server's data directory is deliberately left alone: deleting user data
// is the filesystem layer's job, and the Docker environment likewise only
// removes the container.
func (e *Environment) Destroy() error {
	// Set to stopping then offline to prevent crash detection from firing.
	e.SetState(environment.ProcessStoppingState)

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		e.log().WithField("error", err).Warn("failed to terminate process during destroy")
	}

	e.SetState(environment.ProcessOfflineState)

	if err := os.RemoveAll(e.runtimeDir()); err != nil && !os.IsNotExist(err) {
		return errors.Wrap(err, "environment/native: failed to remove runtime directory")
	}
	return nil
}

// IsAttached reports whether the console is currently being streamed.
func (e *Environment) IsAttached() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cancelAttach != nil
}

// Attach wires up the server console and starts resource polling.
//
// The Docker environment must attach before starting the container to avoid
// missing early output. Here the ordering does not matter: output goes to a
// file, so nothing is lost even if the tail starts late. That also means this
// can attach to a server started by a previous wings process.
func (e *Environment) Attach(ctx context.Context) error {
	if e.IsAttached() {
		return nil
	}

	if err := e.Create(); err != nil {
		return err
	}

	// Hold the write end of the FIFO open for as long as we are attached. This
	// is what stops the server from seeing EOF on stdin the moment it starts:
	// opening a FIFO read-write never blocks and always keeps one writer
	// present, even before the server opens its read end.
	stdin, err := os.OpenFile(e.fifoPath(), os.O_RDWR, 0)
	if err != nil {
		return errors.Wrap(err, "environment/native: failed to open stdin fifo")
	}

	pollCtx, cancel := context.WithCancel(context.Background())

	e.mu.Lock()
	e.stdin = stdin
	e.cancelAttach = cancel
	e.mu.Unlock()

	// Make sure something is watching for the process to exit.
	//
	// Start() sets up that watch for servers it launches, but wings does not go
	// through Start() when it boots and finds a server already running -- it
	// re-attaches instead. Without this, such a server has nothing monitoring
	// it, and the environment would keep reporting it as running long after it
	// had died. adopt() is a no-op if a watch already exists.
	if pid := e.currentPid(); processIsAlive(pid) {
		e.adopt(pid)
	}

	go func() {
		defer func() {
			e.mu.Lock()
			if e.stdin != nil {
				_ = e.stdin.Close()
				e.stdin = nil
			}
			e.cancelAttach = nil
			e.mu.Unlock()
		}()

		go func() {
			if err := e.pollResources(pollCtx); err != nil && !errors.Is(err, context.Canceled) {
				e.log().WithField("error", err).Error("error during environment resource polling")
			}
		}()

		if err := e.tailConsole(pollCtx); err != nil && !errors.Is(err, context.Canceled) {
			e.log().WithField("error", err).Warn("error while streaming console output")
		}
	}()

	return nil
}

// detach tears down the console stream and resource polling.
func (e *Environment) detach() {
	e.mu.Lock()
	cancel := e.cancelAttach
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// tailConsole follows the console log and hands each complete line to the log
// callback, in the same shape the Docker environment delivers container output.
func (e *Environment) tailConsole(ctx context.Context) error {
	f, err := os.Open(e.logPath())
	if err != nil {
		return errors.Wrap(err, "environment/native: failed to open console log")
	}
	defer f.Close()

	var (
		offset int64
		buf    bytes.Buffer
		chunk  = make([]byte, 32*1024)
	)

	emit := func(line []byte) {
		e.logCallbackMx.Lock()
		defer e.logCallbackMx.Unlock()
		if e.logCallback == nil {
			return
		}
		// The callback may retain the slice, so hand it a copy.
		e.logCallback(append([]byte(nil), line...))
	}

	for {
		select {
		case <-ctx.Done():
			// The context is cancelled when the process exits, which is exactly
			// when a server has just written the most interesting thing it will
			// ever write -- a stack trace, "Unable to access jarfile", the EULA
			// notice. Returning here would discard anything produced since the
			// last poll, so drain to EOF first. Without this a server that
			// fails within one poll interval appears to exit silently.
			e.drainConsole(f, &buf, chunk, &offset, emit)
			return ctx.Err()
		default:
		}

		// Detect truncation, which happens every time a server is started.
		if st, err := f.Stat(); err == nil && st.Size() < offset {
			offset = 0
			buf.Reset()
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return errors.Wrap(err, "environment/native: failed to rewind console log")
			}
		}

		n, err := f.Read(chunk)
		if n > 0 {
			offset += int64(n)
			buf.Write(chunk[:n])

			for {
				i := bytes.IndexByte(buf.Bytes(), '\n')
				if i < 0 {
					// Flush an over-long line rather than buffering forever.
					if buf.Len() > maxConsoleLineLength {
						emit(buf.Next(maxConsoleLineLength))
					}
					break
				}
				line := buf.Next(i + 1)
				emit(bytes.TrimRight(line[:len(line)-1], "\r"))
			}
			continue
		}
		if err != nil && err != io.EOF {
			return errors.Wrap(err, "environment/native: error reading console log")
		}

		// Nothing new; wait before checking again.
		//
		// Cancellation is handled by looping back to the top rather than
		// returning here, so that there is a single exit point and it is the
		// one that drains. Returning directly would skip the drain on the most
		// likely path of all: the process exiting while this is asleep.
		select {
		case <-ctx.Done():
			continue
		case <-time.After(consolePollInterval):
		}
	}
}

// drainConsole reads whatever is left in the console log and emits it,
// including a final line with no trailing newline.
//
// Called when the tail is being torn down after the process exited, so that
// its last output is not lost to the race between the process dying and the
// poll loop noticing.
func (e *Environment) drainConsole(f *os.File, buf *bytes.Buffer, chunk []byte, offset *int64, emit func([]byte)) {
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			*offset += int64(n)
			buf.Write(chunk[:n])
			for {
				i := bytes.IndexByte(buf.Bytes(), '\n')
				if i < 0 {
					break
				}
				line := buf.Next(i + 1)
				emit(bytes.TrimRight(line[:len(line)-1], "\r"))
			}
			continue
		}
		if err != nil {
			break
		}
		break
	}
	// A process that died mid-line still said something worth showing.
	if buf.Len() > 0 {
		emit(bytes.TrimRight(buf.Next(buf.Len()), "\r"))
	}
}

// SendCommand writes a command to the server's stdin.
func (e *Environment) SendCommand(c string) error {
	e.mu.RLock()
	stdin := e.stdin
	stop := e.meta.Stop
	e.mu.RUnlock()

	if stdin == nil {
		return errors.Wrap(ErrNotAttached, "environment/native: cannot send command to process")
	}

	// If the command being processed is the same as the process stop command then we
	// want to mark the server as entering the stopping state otherwise the process will
	// stop and Wings will think it has crashed and attempt to restart it.
	if stop.Type == remote.ProcessStopCommand && c == stop.Value {
		e.SetState(environment.ProcessStoppingState)
	}

	if _, err := stdin.Write([]byte(c + "\n")); err != nil {
		return errors.Wrap(err, "environment/native: could not write to process stdin")
	}
	return nil
}

// Readlog returns the last n lines of the console log.
func (e *Environment) Readlog(lines int) ([]string, error) {
	f, err := os.Open(e.logPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "environment/native: failed to open console log")
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, errors.Wrap(err, "environment/native: failed to stat console log")
	}

	// Read backwards from the end in chunks until enough newlines have been
	// seen, so that a multi-gigabyte log does not get pulled into memory.
	const chunkSize = 32 * 1024
	var (
		size      = st.Size()
		pos       = size
		collected []byte
		count     int
	)
	for pos > 0 && count <= lines {
		read := int64(chunkSize)
		if pos < read {
			read = pos
		}
		pos -= read

		chunk := make([]byte, read)
		if _, err := f.ReadAt(chunk, pos); err != nil && err != io.EOF {
			return nil, errors.Wrap(err, "environment/native: failed to read console log")
		}
		count += bytes.Count(chunk, []byte{'\n'})
		collected = append(chunk, collected...)
	}

	out := make([]string, 0, lines)
	for _, line := range bytes.Split(collected, []byte{'\n'}) {
		out = append(out, string(bytes.TrimRight(line, "\r")))
	}
	// Drop the trailing empty element produced by a final newline.
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if len(out) > lines {
		out = out[len(out)-lines:]
	}
	return out, nil
}
