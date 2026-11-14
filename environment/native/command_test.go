//go:build darwin

package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/remote"
)

var stopByCommand = remote.ProcessStopConfiguration{Type: remote.ProcessStopCommand, Value: "stop"}

// startAndCollect starts the environment and returns a function reporting
// whether a line has been seen on its console.
func startAndCollect(t *testing.T, e *Environment) (hasLine func(string) func() bool, firstLine func() string) {
	t.Helper()

	var mu sync.Mutex
	var lines []string
	e.SetLogCallback(func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, strings.TrimSpace(string(b)))
	})
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "process to be running", func() bool {
		ok, _ := e.IsRunning(context.Background())
		return ok
	})

	hasLine = func(want string) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, l := range lines {
				if l == want {
					return true
				}
			}
			return false
		}
	}
	firstLine = func() string {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			if strings.HasPrefix(l, "child:") {
				return l
			}
		}
		return ""
	}
	return hasLine, firstLine
}

// Commands may not contain control characters, other than tabs. The server
// reads its console from a terminal, where they are not just bytes: an
// end-of-file character would close its input for everyone.
func TestNativeEnvironment_SendCommandRejectsControlCharacters(t *testing.T) {
	e := newTestEnvironment(t, echoLoop, stopByCommand)
	hasLine, _ := startAndCollect(t, e)

	for _, c := range []string{"\x04", "say \x13", "\x03", "\x1b[A", "\x7f"} {
		if err := e.SendCommand(c); err == nil {
			t.Errorf("expected command %q to be rejected", c)
		}
	}

	// The console still works afterwards, so nothing above reached it.
	if err := e.SendCommand("say\thello"); err != nil {
		t.Fatalf("send command: %v", err)
	}
	waitFor(t, "the valid command to be echoed", hasLine("got:say\thello"))
}

// A stop typed into the console is a stop, not a crash. Sending the egg's stop
// command is the only thing that marks a server as stopping, so the
// environment has to recognise it.
func TestNativeEnvironment_TypedStopCommandMarksServerAsStopping(t *testing.T) {
	e := newTestEnvironment(t, echoLoop, remote.ProcessStopConfiguration{})
	// As wings does it: the stop configuration arrives after the environment
	// is created, when the server is synced with the Panel.
	e.SetStopConfiguration(stopByCommand)

	startAndCollect(t, e)
	// What the server package does once a server has finished starting.
	e.SetState(environment.ProcessRunningState)

	if err := e.SendCommand("stop"); err != nil {
		t.Fatalf("send command: %v", err)
	}
	// Set before the command is written, so it is already true here. Left at
	// running, the exit that follows is taken for a crash and the server is
	// started again.
	if st := e.State(); st != environment.ProcessStoppingState && st != environment.ProcessOfflineState {
		t.Fatalf("expected the server to be stopping once its stop command was sent, got %s", st)
	}
	waitFor(t, "offline state", func() bool { return e.State() == environment.ProcessOfflineState })
}

// Sending a command times out if the server does not read its input, and such
// a server can still be stopped.
func TestNativeEnvironment_SendCommandDoesNotBlockForever(t *testing.T) {
	previous := commandWriteTimeout
	commandWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { commandWriteTimeout = previous })

	// Never reads its input, and ignores the stop command for the same reason.
	e := newTestEnvironment(t, "sleep 300", stopByCommand)
	startAndCollect(t, e)

	// Fill the terminal's input queue and then the FIFO behind it.
	line := strings.Repeat("x", 255)
	var err error
	start := time.Now()
	for i := 0; i < 4096 && err == nil; i++ {
		err = e.SendCommand(line)
	}
	if !errors.Is(err, errCommandWriteTimeout) {
		t.Fatalf("expected sending to time out once the input was full, got %v after %s", err, time.Since(start))
	}

	// The stop command cannot be delivered either. Asked to terminate, the
	// environment must fall back to killing the server rather than give up.
	done := make(chan error, 1)
	go func() { done <- e.WaitForStop(context.Background(), 5*time.Second, true) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait for stop: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("WaitForStop did not return for a server that does not read its input")
	}
	waitFor(t, "the server to be killed", func() bool {
		ok, _ := e.IsRunning(context.Background())
		return !ok
	})
}

// Commands queued at a server that is not reading its input give up together,
// so a stop command is never left waiting behind them.
func TestNativeEnvironment_QueuedCommandsShareOneTimeout(t *testing.T) {
	previous := commandWriteTimeout
	commandWriteTimeout = 500 * time.Millisecond
	t.Cleanup(func() { commandWriteTimeout = previous })

	e := newTestEnvironment(t, "sleep 300", stopByCommand)
	startAndCollect(t, e)

	line := strings.Repeat("x", 255)
	var err error
	for i := 0; i < 4096 && err == nil; i++ {
		err = e.SendCommand(line)
	}
	if !errors.Is(err, errCommandWriteTimeout) {
		t.Fatalf("expected sending to time out once the input was full, got %v", err)
	}

	// Sent at once, each in its own goroutine, as the websocket does.
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.SendCommand(line)
		}()
	}
	wg.Wait()

	// One timeout between them, not one each.
	if took := time.Since(start); took > 4*commandWriteTimeout {
		t.Fatalf("queued commands took %s to give up, expected about %s", took, commandWriteTimeout)
	}
}

// Anything a server leaves running in the background goes when the server
// does, as it would in a container.
func TestNativeEnvironment_LeftoverProcessesDieWithTheServer(t *testing.T) {
	e := newTestEnvironment(t, `sleep 300 & echo "child:$!"; sleep 1; exit 0`, stopByCommand)
	_, firstLine := startAndCollect(t, e)

	waitFor(t, "child pid announcement", func() bool { return firstLine() != "" })
	var childPid int
	if _, err := fmtSscan(firstLine(), &childPid); err != nil {
		t.Fatalf("could not parse child pid from %q: %v", firstLine(), err)
	}
	if !processIsAlive(childPid) {
		t.Fatalf("child %d should be alive while the server runs", childPid)
	}

	waitFor(t, "the server to exit by itself", func() bool { return e.State() == environment.ProcessOfflineState })
	waitFor(t, "the leftover child to be killed", func() bool { return !processIsAlive(childPid) })
}

// A startup command that is more than one program runs under a shell, and the
// shell dies on a stop signal straight away while the program it started is
// still saving. The server is not stopped until that has finished, and it is
// not killed for outliving its shell.
func TestNativeEnvironment_StopWaitsForWhatTheShellLeftRunning(t *testing.T) {
	root := t.TempDir()
	// The inner shell stands in for a server that takes a second to save when
	// told to stop. The outer one, which is what the supervisor watches, has
	// no handler and is gone the moment the signal arrives.
	startup := `sh -c 'trap "sleep 1; echo saved > saved.txt; exit 0" TERM; echo ready; while :; do sleep 0.1; done' & wait`
	e := newTestEnvironmentAt(t, root, startup, remote.ProcessStopConfiguration{
		Type:  remote.ProcessStopSignal,
		Value: "SIGTERM",
	})
	hasLine, _ := startAndCollect(t, e)
	waitFor(t, "the server to be ready", hasLine("ready"))

	start := time.Now()
	if err := e.WaitForStop(context.Background(), 30*time.Second, false); err != nil {
		t.Fatalf("wait for stop: %v", err)
	}
	if took := time.Since(start); took < 900*time.Millisecond {
		t.Fatalf("the stop was reported after %s, before the server had finished saving", took)
	}
	if _, err := os.Stat(filepath.Join(root, "servers", "test", "saved.txt")); err != nil {
		t.Fatalf("the server was killed before it finished saving: %v", err)
	}
	if st := e.State(); st != environment.ProcessOfflineState {
		t.Fatalf("expected the server to be offline once everything had exited, got %s", st)
	}
}

// The wait above ends when the stop is abandoned for a kill.
func TestNativeEnvironment_KillEndsTheWaitForWhatTheShellLeftRunning(t *testing.T) {
	// Ignores the stop signal entirely, so it would be waited on for good.
	startup := `sh -c 'trap "" TERM; echo "child:$$"; while :; do sleep 0.1; done' & wait`
	e := newTestEnvironment(t, startup, remote.ProcessStopConfiguration{
		Type:  remote.ProcessStopSignal,
		Value: "SIGTERM",
	})
	_, firstLine := startAndCollect(t, e)
	waitFor(t, "child pid announcement", func() bool { return firstLine() != "" })
	var childPid int
	if _, err := fmtSscan(firstLine(), &childPid); err != nil {
		t.Fatalf("could not parse child pid from %q: %v", firstLine(), err)
	}

	if err := e.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The shell at the head is gone; what it started is not, and the server
	// is still stopping.
	time.Sleep(time.Second)
	if !processIsAlive(childPid) {
		t.Fatal("expected the process ignoring the stop signal to still be running")
	}
	if st := e.State(); st != environment.ProcessStoppingState {
		t.Fatalf("expected the server to still be stopping, got %s", st)
	}

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	waitFor(t, "the process to be killed", func() bool { return !processIsAlive(childPid) })
	waitFor(t, "offline state", func() bool { return e.State() == environment.ProcessOfflineState })
}

// Shutting wings down ends every server's context. A server that was being
// stopped at that moment is left for the next wings to pick up, not killed.
func TestNativeEnvironment_WaitForStopLeavesServerRunningWhenWingsShutsDown(t *testing.T) {
	// Ignores its stop command, so the stop is still in progress when the
	// context ends.
	e := newTestEnvironment(t, "sleep 300", stopByCommand)
	startAndCollect(t, e)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.WaitForStop(ctx, time.Minute, true) }()

	waitFor(t, "stopping state", func() bool { return e.State() == environment.ProcessStoppingState })
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected the cancellation to be returned, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("WaitForStop did not return when its context ended")
	}
	if ok, _ := e.IsRunning(context.Background()); !ok {
		t.Fatal("expected the server to be left running")
	}
}
