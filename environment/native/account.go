package native

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// A server is tracked by the process group of its console supervisor, and a
// process can leave a group whenever it likes. One call to setsid or setpgid
// does it, which is what every program that puts itself in the background does
// and what Node does for spawn({detached: true}). Such a process is then out of
// reach of anything addressed to the group: it is not stopped, not killed, not
// throttled and not counted, and it outlives a server shown as offline.
//
// A container has no such way out, because membership there is not something
// the process gets a say in. The nearest thing here is the server's account.
// With system.user.per_server every server runs as a uid of its own, a process
// cannot change its uid, and so everything running as that uid is the server's
// wherever it has moved itself.
//
// None of this is possible without those accounts. Every server then runs as
// the one user wings runs as, along with wings itself and whatever else that
// user has open, and there is nothing to tell one server's stray from any of
// it.

// ownedProcess is a live process running as a server's account.
type ownedProcess struct {
	pid  int
	pgid int
	// stopped is whether it is currently suspended by a signal.
	stopped bool
}

// accountUID returns the uid this server has to itself, and whether it has one.
//
// It answers no for root and for the user wings runs as, whatever the account
// says. Everything that acts on the answer signals every process that uid
// owns, and neither of those could ever be a server's alone.
func (e *Environment) accountUID() (int, bool) {
	e.mu.RLock()
	account := e.meta.Account
	e.mu.RUnlock()

	if account == nil || account.UID <= 0 || account.UID == os.Getuid() || account.UID == os.Geteuid() {
		return 0, false
	}
	return account.UID, true
}

// owned returns every live process running as the server's account, or nothing
// when it does not have one.
func (e *Environment) owned() []ownedProcess {
	uid, ok := e.accountUID()
	if !ok {
		return nil
	}
	procs, err := processesOwnedBy(uid)
	if err != nil {
		e.log().WithField("error", err).Debug("failed to list the processes running as the server's account")
		return nil
	}
	return procs
}

// strays returns the server's processes that are no longer in its process
// group, and so are missed by anything sent to the group.
func (e *Environment) strays(pgid int) []ownedProcess {
	procs := e.owned()
	out := procs[:0]
	for _, p := range procs {
		if p.pgid != pgid {
			out = append(out, p)
		}
	}
	return out
}

// signalStrays sends a signal to each of the server's processes outside its
// process group and returns the pids it was sent to.
//
// The group itself is left to the caller. It is signalled in one call, which
// reaches every member at the same instant; a signal sent here as well would
// reach each of them twice, and for an interrupt that is the difference between
// asking a server to stop and telling it to give up on saving.
func (e *Environment) signalStrays(pgid int, sig unix.Signal) []int {
	var pids []int
	for _, p := range e.strays(pgid) {
		if err := unix.Kill(p.pid, sig); err == nil {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

// accountKillTimeout is the longest spent killing what runs as a server's
// account before giving up on it. A killed process is normally gone within
// milliseconds; this is for the one that is not.
var accountKillTimeout = 10 * time.Second

// accountKillInterval is how long is left between one pass over an account's
// processes and the next.
const accountKillInterval = 10 * time.Millisecond

// killOwnedBy kills every process running as a uid and waits until none is
// left. It returns how many it killed, and how many were still there if it ran
// out of time.
//
// One pass is not enough. A process can fork between being listed and being
// killed, and its child is then on no list. So this goes round until a pass
// finds nothing, which it does quickly: a process is killed far faster than it
// can fork, and the ones killed are no longer there to do it.
func killOwnedBy(uid int) (killed, left int, err error) {
	seen := make(map[int]struct{})
	deadline := time.Now().Add(accountKillTimeout)
	for {
		procs, err := processesOwnedBy(uid)
		if err != nil {
			return len(seen), 0, err
		}
		if len(procs) == 0 {
			return len(seen), 0, nil
		}
		var denied error
		for _, p := range procs {
			switch err := unix.Kill(p.pid, unix.SIGKILL); err {
			case nil:
				seen[p.pid] = struct{}{}
			case unix.EPERM:
				denied = err
			}
		}
		// Not allowed to, which waiting will not change. Only root may signal
		// another user's processes, and a server only has an account when
		// wings is root, so this is not expected to happen at all.
		if denied != nil {
			return len(seen), len(procs), denied
		}
		if !time.Now().Before(deadline) {
			return len(seen), len(procs), nil
		}
		time.Sleep(accountKillInterval)
	}
}

// clearAccount kills everything running as the server's account, and reports
// how many processes that was. It does nothing for a server without one.
//
// The caller must hold accountMu. See there for why.
func (e *Environment) clearAccount() int {
	uid, ok := e.accountUID()
	if !ok {
		return 0
	}
	killed, left, err := killOwnedBy(uid)
	if err != nil || left > 0 {
		e.log().WithField("error", err).WithField("processes", left).Error("could not kill everything running as the server's account; some of it may be left running")
	}
	return killed
}

// sweepAccount kills what a server that has exited left running outside its
// process group. It follows sweepGroup, which has by then waited for and
// killed what was left inside it.
//
// What is outside the group is not waited for. Nothing told it to stop: a stop
// signal goes to the group, and a stop command to the server's console. It is
// killed as a container's other processes are when its main one exits.
//
// wait identifies the process whose exit this is. Killing by account takes no
// notice of which process started what, so this only goes ahead while that
// process is still the one wings is tracking and nobody has cleared the account
// on its behalf already. Otherwise it could be killing the server's next run,
// or its install script.
func (e *Environment) sweepAccount(wait chan struct{}) {
	if _, ok := e.accountUID(); !ok {
		return
	}

	e.accountMu.Lock()
	defer e.accountMu.Unlock()

	e.mu.RLock()
	current := e.wait
	e.mu.RUnlock()
	if current != wait || e.cleared == wait {
		return
	}

	if n := e.clearAccount(); n > 0 {
		e.log().WithField("processes", n).Info("killed processes the server left running outside its process group")
	}
	e.cleared = wait
}

// terminateStrays is the part of Terminate that reaches what has left the
// server's process group.
//
// For a kill that is everything running as the account, until none of it is
// left, so that once Terminate has returned there is nothing of the server
// still running and nothing that will go looking for it later. Any other
// signal is passed on once to what is outside the group, the group having had
// it already.
func (e *Environment) terminateStrays(pgid int, sig unix.Signal) {
	if _, ok := e.accountUID(); !ok {
		return
	}
	if sig != unix.SIGKILL {
		e.signalStrays(pgid, sig)
		return
	}

	e.accountMu.Lock()
	defer e.accountMu.Unlock()

	e.clearAccount()

	// The exit of the process just killed has yet to be noticed, and whoever
	// called this is free to start the server again, or reinstall it, before
	// it is. Record that the account has been dealt with so that sweepAccount
	// does not do it a second time, to whatever is running by then.
	e.mu.RLock()
	e.cleared = e.wait
	e.mu.RUnlock()
}

// SweepLeftovers kills what a server left running if it exited while there was
// no wings to see it.
//
// A server keeps running when wings stops, which is the point, but it can also
// exit then, and nothing sweeps up after it as onProcessExit would have. The
// next wings finds a pid file for a process that is gone and rightly calls the
// server offline, while whatever it left behind runs on. Wings calls this for
// each server it finds not running when it boots.
//
// What was left is identified by the server's account. The pid file is not
// used for it: the pid in it names a process group, and once that group is
// empty the number is free to be given to another, which wings, running as
// root, would then kill. Without an account there is no telling what in that
// group is the server's, so it is reported and left alone.
func (e *Environment) SweepLeftovers() {
	e.accountMu.Lock()
	defer e.accountMu.Unlock()

	// Only for a server with nothing running that wings knows of. One that is
	// running, or whose exit is being dealt with this minute, is not a
	// leftover.
	e.mu.RLock()
	tracking := e.wait != nil
	e.mu.RUnlock()
	if tracking || processIsAlive(e.currentPid()) {
		return
	}

	if _, ok := e.accountUID(); ok {
		if n := e.clearAccount(); n > 0 {
			e.log().WithField("processes", n).Warn("killed processes left running by a server that exited while wings was not running")
		}
		return
	}

	pgid, _, _ := e.recordedPid()
	if pgid <= 0 || processIsAlive(pgid) {
		// No record of a process, or the pid has since gone to something else,
		// which it cannot while anything is left in a group of that number.
		return
	}
	if pids, err := pidsInGroup(pgid); err == nil && len(pids) > 0 {
		e.log().WithField("pids", pids).Warn("processes are running in the process group of a server that exited while wings was not running; without per-server accounts wings cannot tell whether they are the server's, and has left them alone")
	}
}
