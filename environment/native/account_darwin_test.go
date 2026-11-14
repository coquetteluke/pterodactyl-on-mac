//go:build darwin

package native

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/internal/serveruser"
	"github.com/pterodactyl/wings/remote"
)

var stopByKill = remote.ProcessStopConfiguration{Type: remote.ProcessStopSignal, Value: "SIGKILL"}

// unusedUID returns a uid that no account has and nothing is running as, to
// stand in for a server's own account.
//
// It has to be one that really is unused. Stopping a server that has an account
// kills everything running as that account, and every test here stops its
// server when it is done. A number picked by hand would, sooner or later, be
// the uid of something on the machine the tests are run on.
func unusedUID(t *testing.T) int {
	t.Helper()
	for uid := 40000; uid < 41000; uid++ {
		if _, err := user.LookupId(strconv.Itoa(uid)); err == nil {
			continue
		}
		if pids, err := pidsOwnedBy(uid); err != nil || len(pids) > 0 {
			continue
		}
		return uid
	}
	t.Fatal("could not find a uid that is not in use")
	return 0
}

// testAccount returns an account that stands in for a server's own.
func testAccount(t *testing.T) *serveruser.Account {
	t.Helper()
	uid := unusedUID(t)
	return &serveruser.Account{Username: "ptero-test", UID: uid, GID: uid}
}

// newAccountEnvironment builds an environment for a server with an account of
// its own, and returns it with the server's data directory. Running anything
// as another user takes root, so the test is skipped without it.
func newAccountEnvironment(t *testing.T, startup string, stop remote.ProcessStopConfiguration) (*Environment, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("changing a process's user requires root")
	}

	// t.TempDir() is 0700 and root-owned, which the dropped-privilege process
	// cannot even traverse into, so use a path it can reach.
	root, err := os.MkdirTemp("/private/tmp", "ptero-iso")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	// Registered before the environment's own cleanup, so it runs after the
	// server has been stopped.
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	e := newTestEnvironmentAt(t, root, startup, stop)
	e.meta.Account = testAccount(t)

	dir, err := e.workingDir()
	if err != nil {
		t.Fatalf("working dir: %v", err)
	}
	for _, p := range []string{root, filepath.Dir(dir)} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}
	// The account has to be able to write in its own data directory, which is
	// what the filesystem layer's chown does in production.
	if err := os.Chown(dir, e.meta.Account.UID, e.meta.Account.GID); err != nil {
		t.Fatalf("chown server dir: %v", err)
	}
	return e, dir
}

// detachedHelper is a command that starts a process which leaves the server's
// process group and then burns a core. See runDetachedHelper.
func detachedHelper(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	return shellQuote(exe) + " " + detachedHelperArg
}

// detachedPid waits for the helper started in dir to have left its group and
// returns its pid.
func detachedPid(t *testing.T, dir string) int {
	t.Helper()
	var pid int
	waitFor(t, "the detached process to announce itself", func() bool {
		b, err := os.ReadFile(filepath.Join(dir, detachedPidFile))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	return pid
}

// startWithDetached starts a server whose startup command is to run the
// detached helper beside a shell that waits, and returns the supervisor's pid
// and the helper's.
func startWithDetached(t *testing.T, e *Environment, dir string) (leader, detached int) {
	t.Helper()
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "process to be running", func() bool {
		ok, _ := e.IsRunning(context.Background())
		return ok
	})
	leader = e.currentPid()
	detached = detachedPid(t, dir)

	// The premise of every test that uses this. Were the helper still in the
	// group, the group's own handling would account for whatever follows.
	bi, err := pidBSDInfo(detached)
	if err != nil {
		t.Fatalf("detached process %d: %v", detached, err)
	}
	if int(bi.PGid) == leader {
		t.Fatalf("process %d is still in the server's process group %d", detached, leader)
	}
	return leader, detached
}

// running reports whether a pid is a process that is still running. Unlike
// processIsAlive it says no for one that has been killed and is only waiting
// to be reaped, so it can be asked the moment a kill has returned.
func running(pid int) bool {
	bi, err := pidBSDInfo(pid)
	return err == nil && bi.Status != szomb
}

// resourceEvents returns the resource samples an environment publishes.
func resourceEvents(e *Environment) <-chan environment.Stats {
	raw := make(chan []byte, 32)
	e.Events().On(raw)

	stats := make(chan environment.Stats, 8)
	go func() {
		for b := range raw {
			var ev struct {
				Topic string
				Data  environment.Stats
			}
			if err := json.Unmarshal(b, &ev); err != nil || ev.Topic != environment.ResourceEvent {
				continue
			}
			select {
			case stats <- ev.Data:
			default:
			}
		}
	}()
	return stats
}

// The struct has to line up with the kernel's. The fields at either end were
// already relied on; the owner and the group in the middle are what decides
// who gets signalled.
func TestPidBSDInfoDescribesThisProcess(t *testing.T) {
	bi, err := pidBSDInfo(os.Getpid())
	if err != nil {
		t.Fatalf("pidBSDInfo: %v", err)
	}
	for _, f := range []struct {
		name      string
		got, want int
	}{
		{"pid", int(bi.Pid), os.Getpid()},
		{"parent pid", int(bi.PPid), os.Getppid()},
		{"effective uid", int(bi.UID), os.Geteuid()},
		{"real uid", int(bi.RUID), os.Getuid()},
		{"effective gid", int(bi.GID), os.Getegid()},
		{"real gid", int(bi.RGID), os.Getgid()},
		{"process group", int(bi.PGid), unix.Getpgrp()},
	} {
		if f.got != f.want {
			t.Errorf("%s: got %d, expected %d", f.name, f.got, f.want)
		}
	}
	if bi.Status == sstop || bi.Status == szomb {
		t.Errorf("a running process reported status %d", bi.Status)
	}
	started := time.Unix(int64(bi.StartSec), int64(bi.StartUsec)*1000)
	if started.After(time.Now()) || time.Since(started) > 24*time.Hour {
		t.Errorf("implausible start time %s", started)
	}
}

// Listing by owner has to find a process that has left its group, which is the
// only reason it exists, and has to tell which group it is in now.
func TestProcessesOwnedByFindsAProcessOutsideItsGroup(t *testing.T) {
	// Setsid puts it in a session and a group of its own, as the helper does
	// to itself.
	cmd := exec.Command("/bin/sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	if os.Getuid() == 0 {
		// Root's processes are never listed for signalling.
		if procs, err := processesOwnedBy(0); err == nil {
			t.Fatalf("expected root's processes to be refused, got %d of them", len(procs))
		}
		return
	}

	procs, err := processesOwnedBy(os.Getuid())
	if err != nil {
		t.Fatalf("processesOwnedBy: %v", err)
	}
	var found bool
	for _, p := range procs {
		if p.pid == os.Getpid() {
			t.Error("wings' own process was listed among those to signal")
		}
		if p.pid != pid {
			continue
		}
		found = true
		if p.pgid != pid {
			t.Errorf("expected process %d to be reported in its own group, got group %d", pid, p.pgid)
		}
		if p.stopped {
			t.Errorf("process %d reported as stopped while running", pid)
		}
	}
	if !found {
		t.Fatalf("process %d, outside this process's group, was not among the %d listed for uid %d", pid, len(procs), os.Getuid())
	}

	// A process that is stopped says so, which is what decides whether it is
	// sent a resume.
	if err := unix.Kill(pid, unix.SIGSTOP); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, "the process to be reported as stopped", func() bool {
		procs, _ := processesOwnedBy(os.Getuid())
		for _, p := range procs {
			if p.pid == pid {
				return p.stopped
			}
		}
		return false
	})

	// One that has exited and not been reaped is not there to be signalled.
	if err := unix.Kill(pid, unix.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}
	waitFor(t, "the dead process to drop out of the listing", func() bool {
		procs, _ := processesOwnedBy(os.Getuid())
		for _, p := range procs {
			if p.pid == pid {
				return false
			}
		}
		return true
	})
}

// Without an account a server shares its uid with every other server, with
// wings, and with whatever else that user is running. Nothing may be done by
// uid then. A process that leaves the group is out of reach, and has to stay
// out of reach.
func TestNativeEnvironment_WithoutAnAccountNothingIsSignalledByUid(t *testing.T) {
	e := newTestEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	dir, err := e.workingDir()
	if err != nil {
		t.Fatalf("working dir: %v", err)
	}
	leader, detached := startWithDetached(t, e, dir)
	defer func() { _ = unix.Kill(detached, unix.SIGKILL) }()

	if _, cpu, err := e.sampleGroup(leader); err != nil {
		t.Fatalf("sample: %v", err)
	} else if _, counted := cpu[detached]; counted {
		t.Error("a process outside the group was counted for a server with no account of its own")
	}

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	waitFor(t, "the server to be killed", func() bool { return !processIsAlive(leader) })
	waitFor(t, "offline state", func() bool { return e.State() == environment.ProcessOfflineState })

	e.SweepLeftovers()

	if !running(detached) {
		t.Fatal("a process outside the group was killed for a server with no account of its own; nothing says it was the server's")
	}
}

// The pid file of a server that exited while wings was not running names a
// process group. Without an account there is no telling whether what is in
// that group now is the server's, so it is not touched.
func TestNativeEnvironment_StalePidFileDoesNotGetItsGroupKilled(t *testing.T) {
	e := newTestEnvironment(t, "sleep 1", stopByKill)
	if err := e.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A group whose leader has gone and left a member behind.
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $!; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pgid := cmd.Process.Pid
	defer func() { _ = unix.Kill(-pgid, unix.SIGKILL) }()

	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	member, err := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	if err != nil {
		t.Fatalf("could not read the member's pid from %q: %v", buf[:n], err)
	}

	if err := e.writePidFile(pgid); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	_, _ = cmd.Process.Wait()

	if running, _ := e.IsRunning(context.Background()); running {
		t.Fatal("expected the server to be reported as not running")
	}
	e.SweepLeftovers()

	if !running(member) {
		t.Fatal("a process was killed on the strength of the pid file alone")
	}
}

// A process that leaves the server's group is still the server's when the
// server has an account of its own: it is counted in the server's usage, and
// the kill button kills it.
func TestNativeEnvironment_DedicatedAccountTerminateKillsWhatLeftTheGroup(t *testing.T) {
	e, dir := newAccountEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	stats := resourceEvents(e)
	leader, detached := startWithDetached(t, e, dir)

	_, cpu, err := e.sampleGroup(leader)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if _, counted := cpu[detached]; !counted {
		t.Fatalf("process %d, outside the group, is missing from the server's usage: %v", detached, cpu)
	}

	// And in what the Panel is told. The helper burns a core and nothing in
	// the group does anything at all, so any real CPU figure is the helper's.
	deadline := time.After(15 * time.Second)
	for counted := false; !counted; {
		select {
		case s := <-stats:
			counted = s.CpuAbsolute >= 20
		case <-deadline:
			t.Fatal("timed out waiting for the detached process's cpu to show in the server's stats")
		}
	}

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	// Terminate does not return until it is done, so there is nothing to
	// wait for here beyond the process being reaped.
	waitFor(t, "the detached process to be killed", func() bool { return !processIsAlive(detached) })
	waitFor(t, "the server to be killed", func() bool { return !processIsAlive(leader) })
	waitFor(t, "offline state", func() bool { return e.State() == environment.ProcessOfflineState })
}

// The same when the server exits by itself, as everything in a container goes
// when its main process does.
func TestNativeEnvironment_DedicatedAccountExitSweepsWhatLeftTheGroup(t *testing.T) {
	// The shell waits for the helper to be out of the group before it exits,
	// so that the group sweep cannot be what kills it.
	e, dir := newAccountEnvironment(t,
		detachedHelper(t)+` & while [ ! -s `+detachedPidFile+` ]; do sleep 0.1; done; exit 0`,
		stopByCommand)
	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	detached := detachedPid(t, dir)

	waitFor(t, "the server to exit by itself", func() bool { return e.State() == environment.ProcessOfflineState })
	// The server is not offline until the sweep is done.
	waitFor(t, "the detached process to be killed", func() bool { return !processIsAlive(detached) })
}

// Deleting a server leaves nothing of it running.
func TestNativeEnvironment_DedicatedAccountDestroyKillsWhatLeftTheGroup(t *testing.T) {
	e, dir := newAccountEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	leader, detached := startWithDetached(t, e, dir)

	if err := e.Destroy(); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	waitFor(t, "the detached process to be killed", func() bool { return !processIsAlive(detached) })
	waitFor(t, "the server to be killed", func() bool { return !processIsAlive(leader) })
	if _, err := os.Stat(e.runtimeDir()); !os.IsNotExist(err) {
		t.Fatalf("expected the runtime directory to be removed, got %v", err)
	}
}

// abandon has an environment forget the server it is running, without touching
// the server, and then kills the server's process group. That is what a server
// exiting while wings is not running looks like to the next wings: a pid file
// for a process that has gone, and nobody having swept up after it.
func abandon(t *testing.T, e *Environment, leader int) {
	t.Helper()
	e.mu.Lock()
	e.pid = 0
	e.wait = nil
	e.mu.Unlock()
	e.detach()

	if err := unix.Kill(-leader, unix.SIGKILL); err != nil {
		t.Fatalf("kill group: %v", err)
	}
	waitFor(t, "the server's process group to be gone", func() bool { return !processIsAlive(leader) })
}

// A server that exits while wings is not running leaves whatever it had put
// outside its group running, with a pid file that names nothing. The next
// wings clears it out when it boots and finds the server not running.
func TestNativeEnvironment_DedicatedAccountLeftoversAreSweptWhenWingsBoots(t *testing.T) {
	e, dir := newAccountEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	leader, detached := startWithDetached(t, e, dir)
	abandon(t, e, leader)

	if !running(detached) {
		t.Fatal("expected the detached process to outlive the server's process group")
	}

	// A second environment over the same on-disk state, as a restarted wings
	// builds.
	next, err := New(e.Id, &Metadata{Account: e.meta.Account}, e.Configuration)
	if err != nil {
		t.Fatalf("second environment: %v", err)
	}
	if running, _ := next.IsRunning(context.Background()); running {
		t.Fatal("expected the server to be reported as not running")
	}
	next.SweepLeftovers()

	// SweepLeftovers does not return until it is done.
	waitFor(t, "the leftover process to be killed", func() bool { return !processIsAlive(detached) })
}

// And a server is never started beside what an earlier run of it left behind.
func TestNativeEnvironment_DedicatedAccountStartClearsWhatAnEarlierRunLeft(t *testing.T) {
	e, dir := newAccountEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	leader, detached := startWithDetached(t, e, dir)
	abandon(t, e, leader)

	if err := os.Remove(filepath.Join(dir, detachedPidFile)); err != nil {
		t.Fatalf("remove pid file: %v", err)
	}
	_, again := startWithDetached(t, e, dir)
	if again == detached {
		t.Fatalf("expected a new detached process, got %d again", again)
	}
	if running(detached) {
		t.Fatal("the server was started with a process from its earlier run still running")
	}
	if !running(again) {
		t.Fatal("the new run's own process was killed")
	}
}

// Killing by account takes no notice of which run of the server started what.
// The exit of one run is noticed a moment after it is killed, by which time the
// server may have been started again; sweeping up after the old run must not
// take the new one with it.
func TestNativeEnvironment_DedicatedAccountSweepSparesALaterRun(t *testing.T) {
	e, dir := newAccountEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	startWithDetached(t, e, dir)

	e.mu.RLock()
	first := e.wait
	e.mu.RUnlock()

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	waitFor(t, "the first run's exit to be dealt with", func() bool {
		e.mu.RLock()
		defer e.mu.RUnlock()
		return e.wait == nil
	})

	if err := os.Remove(filepath.Join(dir, detachedPidFile)); err != nil {
		t.Fatalf("remove pid file: %v", err)
	}
	leader, detached := startWithDetached(t, e, dir)

	// The first run's exit, noticed late.
	e.sweepAccount(first)
	if !running(leader) || !running(detached) {
		t.Fatal("sweeping up after the first run killed the second")
	}

	// And a run whose account Terminate has already cleared is not swept a
	// second time when its exit is noticed. Whoever called Terminate may have
	// started an install by then, as the same account.
	e.mu.RLock()
	second := e.wait
	e.mu.RUnlock()
	e.accountMu.Lock()
	e.cleared = second
	e.accountMu.Unlock()

	e.sweepAccount(second)
	if !running(leader) || !running(detached) {
		t.Fatal("the account was swept again after Terminate had cleared it")
	}
}

// What is counted against the CPU limit has to be what is throttled for going
// over it. A process outside the group that was counted and left running would
// have the rest of the server stopped on its behalf.
func TestCPULimit_DedicatedAccountHoldsWhatLeftTheGroupToTheLimit(t *testing.T) {
	e, dir := newAccountEnvironment(t, detachedHelper(t)+" & wait", stopByKill)
	const limit = 40 // percent of one core
	setCPULimit(t, e, limit, true)
	leader, detached := startWithDetached(t, e, dir)

	// Let the controller settle, then measure over a window.
	time.Sleep(1500 * time.Millisecond)
	_, before, err := e.sampleGroup(leader)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	start := time.Now()
	time.Sleep(3 * time.Second)
	_, after, err := e.sampleGroup(leader)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	usage := float64(cpuDelta(before, after)) / float64(time.Since(start).Nanoseconds()) * 100
	t.Logf("held at %.1f%% against a %d%% limit", usage, limit)

	// The same generous bounds as for a server that stays in its group.
	if usage > limit*2 {
		t.Fatalf("server ran at %.1f%%, far above its %d%% limit", usage, limit)
	}
	if usage < 5 {
		t.Fatalf("server ran at %.1f%%, which suggests it was throttled to a standstill", usage)
	}

	// And it is not left stopped once the limiter has gone.
	e.detach()
	time.Sleep(500 * time.Millisecond)
	if processIsStopped(detached) {
		t.Fatal("the detached process was left stopped after the limiter shut down")
	}
}
