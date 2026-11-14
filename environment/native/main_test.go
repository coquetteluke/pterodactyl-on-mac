//go:build darwin

package native

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/internal/consolepty"
)

// consoleSupervisorArg marks a re-execution of the test binary that should act
// as the console supervisor instead of running tests.
//
// The environment starts a server by re-executing the wings binary with a
// hidden subcommand, which under `go test` is not wings but the test binary.
// Left alone it would run the entire suite again, once per server started,
// until the machine ran out of processes.
const consoleSupervisorArg = "-native-console-supervisor"

// detachedHelperArg marks a re-execution of the test binary that should leave
// the process group it was started in, as a program that puts itself in the
// background does, and then stay busy.
//
// macOS has no setsid(1) to do this from a startup command, and the test
// binary is already somewhere a server's account can run it from.
const detachedHelperArg = "-native-detached-helper"

// detachedPidFile is where the helper records its pid, in the directory it was
// started in, once it is out of the group.
const detachedPidFile = "detached.pid"

func runDetachedHelper() {
	// A new session, and with it a new process group.
	if _, err := unix.Setsid(); err != nil {
		fmt.Fprintln(os.Stderr, "setsid:", err)
		os.Exit(1)
	}
	// Renamed into place so that a reader never sees it half written.
	tmp := detachedPidFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "pid file:", err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, detachedPidFile); err != nil {
		fmt.Fprintln(os.Stderr, "pid file:", err)
		os.Exit(1)
	}
	// Burn a core, so that it shows in the server's usage if it is counted.
	// Not for ever: a test that fails to kill this must not leave it running.
	for start := time.Now(); time.Since(start) < 2*time.Minute; {
	}
	os.Exit(0)
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == detachedHelperArg {
		runDetachedHelper()
	}
	if len(os.Args) > 1 && os.Args[1] == consoleSupervisorArg {
		code, err := consolepty.Run(consolepty.Config{
			Args: os.Args[2:],
			In:   os.Stdin,
			Out:  os.Stdout,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(code)
	}

	// Point the environment at the branch above. Servers started by the tests
	// then run behind a real pseudo-terminal, which is the arrangement being
	// tested: console output arriving through a terminal, signals reaching a
	// group with the supervisor at its head, and resources sampled across it.
	consoleSupervisorArgv = func() ([]string, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return []string{exe, consoleSupervisorArg}, nil
	}

	os.Exit(m.Run())
}
