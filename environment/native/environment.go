// Package native implements a wings ProcessEnvironment that runs a server as
// an ordinary operating system process rather than inside a Docker container.
//
// It exists because macOS cannot run Linux containers. Every "Docker for Mac"
// style runtime -- Docker Desktop, Colima, OrbStack, Rancher -- boots a Linux
// virtual machine, and Apple's own container framework runs a micro-VM per
// container and requires Apple silicon. Running the server process directly is
// the only way to host a Pterodactyl node on a Mac without a VM.
//
// What this environment gives up relative to Docker, in exchange:
//
//   - Resource limits are advisory. macOS has no cgroups, so the memory and
//     CPU numbers from the Panel are reported back for display but not
//     enforced by the kernel. For a JVM server the -Xmx flag in the startup
//     command is the real memory ceiling. See InSituUpdate.
//   - There is no filesystem or network namespace. The process sees the host's
//     filesystem, subject only to normal unix permissions, and binds host ports
//     directly instead of going through port mapping.
//   - The egg's Docker image is not used to supply an interpreter. Whatever the
//     startup command invokes must exist on the host's PATH.
//
// In exchange the server process survives a wings restart, which a plain
// exec-and-hold-the-pipes implementation would not: stdin is a FIFO and stdout
// is a log file, both of which outlive the wings process, and the pid is
// recorded so a later wings can adopt the running server.
package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"emperror.dev/errors"
	"github.com/apex/log"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/events"
	"github.com/pterodactyl/wings/remote"
	"github.com/pterodactyl/wings/system"
)

type Metadata struct {
	// Image is the egg's Docker image. The native environment never pulls or
	// runs it, but the Panel still sends it and the value is surfaced in logs
	// to make it obvious which egg a server came from.
	Image string

	// Stop describes how the Panel wants this server stopped.
	Stop remote.ProcessStopConfiguration
}

// Ensure that the native environment is always implementing all the methods
// from the base environment interface.
var _ environment.ProcessEnvironment = (*Environment)(nil)

type Environment struct {
	mu sync.RWMutex

	// Id is the server UUID. It names the runtime directory and is what the
	// Panel uses to refer to this instance.
	Id string

	// The environment configuration.
	Configuration *environment.Configuration

	meta *Metadata

	// pid of the running server process, or 0 when offline. This is the leader
	// of its own process group; signals are sent to the whole group so that a
	// wrapper script does not leave the real server orphaned.
	pid int

	// stdin is the write end of the console FIFO. It is held open for the
	// lifetime of the process so that the server never sees EOF on stdin.
	stdin *os.File

	// exitCode of the last process to terminate.
	exitCode uint32

	// wait is closed when the running process is reaped, letting WaitForStop
	// block without polling.
	wait chan struct{}

	// cancelAttach tears down the console tail and the resource poller.
	cancelAttach context.CancelFunc

	emitter *events.Bus

	logCallbackMx sync.Mutex
	logCallback   func([]byte)

	// Tracks the environment state.
	st *system.AtomicString
}

// New creates a new native process environment. The id is the server UUID and
// is used to namespace the runtime directory holding the console log, the
// stdin FIFO and the pid file.
func New(id string, m *Metadata, c *environment.Configuration) (*Environment, error) {
	e := &Environment{
		Id:            id,
		Configuration: c,
		meta:          m,
		st:            system.NewAtomicString(environment.ProcessOfflineState),
		emitter:       events.NewBus(),
	}
	return e, nil
}

func (e *Environment) log() *log.Entry {
	return log.WithField("environment", e.Type()).WithField("server", e.Id)
}

func (e *Environment) Type() string {
	return "native"
}

// runtimeDir is where wings keeps the plumbing for this server: the console
// log, the stdin FIFO and the pid file.
//
// It deliberately sits outside the server's data directory, which is exposed
// over SFTP and swept up by backups. Users have no business seeing it and a
// restored backup must not carry a stale pid file with it.
func (e *Environment) runtimeDir() string {
	return filepath.Join(config.Get().System.RootDirectory, "native", e.Id)
}

func (e *Environment) logPath() string   { return filepath.Join(e.runtimeDir(), "console.log") }
func (e *Environment) fifoPath() string  { return filepath.Join(e.runtimeDir(), "stdin") }
func (e *Environment) pidPath() string   { return filepath.Join(e.runtimeDir(), "pid") }

// workingDir returns the server's data directory.
//
// environment.Mount documents the "Default" mount as the server root for
// non-container environments, which is exactly what this is; the remaining
// mounts describe extra bind mounts that only mean something inside a
// container and are ignored here.
func (e *Environment) workingDir() (string, error) {
	for _, m := range e.Configuration.Mounts() {
		if m.Default {
			return m.Source, nil
		}
	}
	return "", errors.New("environment/native: server has no default mount to use as its working directory")
}

// Events returns an event bus for the environment.
func (e *Environment) Events() *events.Bus {
	return e.emitter
}

// Config returns the environment configuration allowing a process to make
// modifications of the environment on the fly.
func (e *Environment) Config() *environment.Configuration {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.Configuration
}

// SetStopConfiguration sets the stop configuration for the environment.
func (e *Environment) SetStopConfiguration(c remote.ProcessStopConfiguration) {
	e.mu.Lock()
	e.meta.Stop = c
	e.mu.Unlock()
}

func (e *Environment) SetImage(i string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.meta.Image = i
}

// Exists reports whether the environment is present and bootable.
//
// The Docker environment checks for a container. There is no equivalent object
// here -- a process either runs or it does not -- so the closest meaningful
// check is that the server's data directory exists.
func (e *Environment) Exists() (bool, error) {
	dir, err := e.workingDir()
	if err != nil {
		return false, err
	}
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "environment/native: failed to stat server directory")
	}
	if !st.IsDir() {
		return false, errors.New("environment/native: server data path exists but is not a directory")
	}
	return true, nil
}

// IsRunning determines whether a server process is currently alive.
//
// The pid is consulted from disk rather than from memory so that this stays
// correct for a server started by a previous wings process.
func (e *Environment) IsRunning(ctx context.Context) (bool, error) {
	return processIsAlive(e.currentPid()), nil
}

// currentPid returns the pid wings believes is running, preferring in-memory
// state and falling back to the pid file so a restarted wings can still find
// a server it did not start.
func (e *Environment) currentPid() int {
	e.mu.RLock()
	pid := e.pid
	e.mu.RUnlock()
	if pid > 0 {
		return pid
	}
	return e.readPidFile()
}

// readPidFile returns the recorded pid, but only if it still refers to the same
// process wings started.
//
// A bare pid is not enough. Pids are recycled, so a stale file can point at
// something else entirely, and wings would then report a long-dead server as
// running and refuse to boot it. The file therefore also records the process
// start time, which the kernel supplies and which no recycled pid can match.
func (e *Environment) readPidFile() int {
	b, err := os.ReadFile(e.pidPath())
	if err != nil {
		return 0
	}
	var (
		pid     int
		startNS int64
	)
	// Older files hold just a pid; accept them rather than orphaning a running
	// server across an upgrade, but they get no reuse protection.
	n, _ := fmt.Sscanf(string(b), "%d %d", &pid, &startNS)
	if n < 1 || pid <= 0 {
		return 0
	}
	if n < 2 {
		return pid
	}

	started, err := pidStartTime(pid)
	if err != nil {
		return 0
	}
	if started.UnixNano() != startNS {
		// The pid has been recycled; whatever holds it now is not our server.
		return 0
	}
	return pid
}

// writePidFile records the pid alongside the process start time, so a later
// wings can tell our server apart from an unrelated process that happens to
// have inherited the same pid. See readPidFile.
func (e *Environment) writePidFile(pid int) error {
	var startNS int64
	if started, err := pidStartTime(pid); err == nil {
		startNS = started.UnixNano()
	}
	return os.WriteFile(e.pidPath(), []byte(fmt.Sprintf("%d %d\n", pid, startNS)), 0o600)
}

// ExitState returns the exit code of the last process to run, and whether it
// was killed by the system for running out of memory.
//
// The second value is always false: macOS has no OOM killer in the Linux
// sense. A JVM that exhausts its heap exits on its own with a non-zero status
// and is reported as an ordinary crash.
func (e *Environment) ExitState() (uint32, bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.exitCode, false, nil
}

// InSituUpdate applies changed resource limits to a running server.
//
// This is a no-op. macOS has no cgroups, so there is nothing to update: the
// limits the Panel sends are reported back for display but never enforced. The
// call succeeds so that wings still persists the new configuration to disk,
// which matches how the Docker environment behaves when a container is absent.
func (e *Environment) InSituUpdate() error {
	return nil
}

func (e *Environment) State() string {
	return e.st.Load()
}

// SetState sets the state of the environment. This emits an event that server's
// can hook into to take their own actions and track their own state based on
// the environment.
func (e *Environment) SetState(state string) {
	if state != environment.ProcessOfflineState &&
		state != environment.ProcessStartingState &&
		state != environment.ProcessRunningState &&
		state != environment.ProcessStoppingState {
		panic(errors.New(fmt.Sprintf("invalid server state received: %s", state)))
	}

	// Emit the event to any listeners that are currently registered.
	if e.State() != state {
		// If the state changed make sure we update the internal tracking to note that.
		e.st.Store(state)
		e.Events().Publish(environment.StateChangeEvent, state)
	}
}

func (e *Environment) SetLogCallback(f func([]byte)) {
	e.logCallbackMx.Lock()
	defer e.logCallbackMx.Unlock()

	e.logCallback = f
}

// Uptime returns how long the current server process has been running, in
// milliseconds. A stopped server reports zero.
func (e *Environment) Uptime(ctx context.Context) (int64, error) {
	pid := e.currentPid()
	if !processIsAlive(pid) {
		return 0, nil
	}
	started, err := pidStartTime(pid)
	if err != nil {
		return 0, errors.Wrap(err, "environment/native: could not read process start time")
	}
	return time.Since(started).Milliseconds(), nil
}
