// Package consolepty runs a server process behind a pseudo-terminal.
//
// Programs decide whether to emit colour by asking isatty(3) about their own
// stdout. The native environment hands the process a log file, which is not a
// terminal, so every server started this way prints in plain text -- unlike the
// Docker environment, which sets Tty on the container and therefore gets a pty
// for free.
//
// A pty cannot simply be opened by wings and handed over, because a pty has
// exactly one master and the kernel tears the line down as soon as that master
// is closed. Were wings to hold it, restarting wings would close the master and
// the next thing a running server wrote to stdout would fail with EIO. Servers
// surviving a wings restart is a property this fork deliberately has, so the
// master is instead held by this supervisor: a short-lived child of wings that
// is detached into its own session and outlives the wings process that spawned
// it.
//
// The resulting arrangement mirrors what the console already expects:
//
//	wings --FIFO--> supervisor --pty--> server
//	                    `-----> console.log --> wings tails --> Panel
package consolepty

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"emperror.dev/errors"
	"github.com/creack/pty"
)

// Terminal dimensions reported to the server.
//
// Nothing is attached to the other end of this terminal, so the only thing the
// size changes is how the programs that consult it lay their output out. It is
// deliberately wider than a default 80x24 terminal: line editors such as JLine,
// which Minecraft servers use, wrap on the reported width, and wrapping a long
// log line at 80 columns is an artefact the Panel console would then show
// forever.
const (
	defaultCols = 200
	defaultRows = 50
)

// drainGrace bounds how long the supervisor keeps reading the terminal after
// the server has exited.
//
// Output written immediately before exiting is still sitting in the terminal
// buffer at that point, and it is the most interesting output a server ever
// produces -- the stack trace, the "Unable to access jarfile". Reading stops at
// end-of-stream well within this window; the timeout only matters if some other
// process inherited the terminal and is holding it open, in which case exiting
// is better than hanging around forever.
const drainGrace = 500 * time.Millisecond

// Config describes the process to supervise.
type Config struct {
	// Args is the command to run, as an argv slice.
	Args []string

	// In is where console input is read from, and is the read end of the
	// server's stdin FIFO. Anything arriving here is written to the terminal,
	// so the server reads it as keyboard input. May be nil, in which case the
	// server simply never receives input.
	In *os.File

	// Out is where console output is written, and is the server's console log.
	Out *os.File
}

// Run starts the configured process behind a pseudo-terminal, relays its output
// to c.Out until it exits, and returns the code it exited with.
//
// The exit code follows the shell convention for a process killed by a signal,
// reporting 128+signal, so that the value can be handed to the Panel unchanged.
func Run(c Config) (int, error) {
	if len(c.Args) == 0 {
		return 0, errors.New("internal/consolepty: no command to run")
	}
	if c.Out == nil {
		return 0, errors.New("internal/consolepty: no output destination")
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		// A server that runs without colour is better than a server that does
		// not run, so this degrades to what the environment did before rather
		// than failing the boot. The note goes to the console log because that
		// is the one place an operator is certain to look.
		_, _ = io.WriteString(c.Out, "[Wings] could not allocate a terminal, console colour is disabled: "+err.Error()+"\n")
		return runWithoutTerminal(c)
	}
	defer ptmx.Close()

	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: defaultCols, Rows: defaultRows}); err != nil {
		// Only affects layout, so it is not worth refusing to start over.
		_, _ = io.WriteString(c.Out, "[Wings] could not set the terminal size: "+err.Error()+"\n")
	}

	cmd := exec.Command(c.Args[0], c.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty

	// Deliberately no Setsid here.
	//
	// wings put this supervisor in a session of its own and addresses stop
	// signals to the whole process group, relying on the group leader's pid
	// being the one it recorded. Giving the server its own session would move
	// it out of that group and every signal the Panel sends would then miss it.
	//
	// The cost is that the terminal is not the server's controlling terminal,
	// which costs nothing here: colour depends on isatty, not on job control.

	// Catch the signals wings aims at the process group, so that the supervisor
	// outlives the server it is supervising. If it died first the master would
	// close, and a server working through a graceful shutdown would lose its
	// stdout mid-sentence.
	//
	// This is signal.Notify and not signal.Ignore on purpose. Ignored signals
	// survive execve and would be inherited by the server, which would then sit
	// there ignoring the Panel's stop button; caught signals are reset to their
	// default on exec, which is what the server needs. Notifying before the
	// process starts closes the window where a stop arriving early would kill
	// the supervisor out from under it.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	defer func() {
		// Stop first: once it returns the signal package will not send again,
		// which is what makes closing the channel safe and lets the goroutine
		// below finish rather than sitting on a channel forever.
		signal.Stop(sigs)
		close(sigs)
	}()
	go func() {
		for range sigs {
			// Discarded. The server is in the same process group and has
			// received the signal directly.
		}
	}()

	if err := cmd.Start(); err != nil {
		_ = tty.Close()
		return 0, errors.Wrap(err, "internal/consolepty: failed to start process")
	}

	// Drop this process's handle on the terminal now that the server holds one.
	// The read below ends at end-of-stream when the last handle closes, and
	// keeping one here would mean that never happens.
	_ = tty.Close()

	if c.In != nil {
		go func() { _, _ = io.Copy(ptmx, c.In) }()
	}

	copied := make(chan struct{})
	go func() {
		defer close(copied)
		// Ends in EIO on darwin once the last terminal handle is gone, which is
		// the normal way for this to finish rather than an error worth
		// reporting.
		_, _ = io.Copy(c.Out, ptmx)
	}()

	code := wait(cmd)

	select {
	case <-copied:
	case <-time.After(drainGrace):
	}

	return code, nil
}

// runWithoutTerminal runs the process with its output going straight to the
// console log, which is what the environment did before pseudo-terminals were
// introduced. Reached only when a terminal could not be allocated.
func runWithoutTerminal(c Config) (int, error) {
	cmd := exec.Command(c.Args[0], c.Args[1:]...)
	cmd.Stdout, cmd.Stderr = c.Out, c.Out
	if c.In != nil {
		cmd.Stdin = c.In
	}
	if err := cmd.Start(); err != nil {
		return 0, errors.Wrap(err, "internal/consolepty: failed to start process")
	}
	return wait(cmd), nil
}

// wait reaps the process and reduces its exit status to a single code, using
// the shell's 128+signal convention for one that was killed.
func wait(cmd *exec.Cmd) int {
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
	}
	// Something went wrong that was not the process reporting a status; treat
	// it as a failure rather than a clean exit so crash detection still fires.
	return 1
}
