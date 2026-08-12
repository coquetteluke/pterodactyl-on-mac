//go:build darwin

package native

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// pidsInGroup has to genuinely enumerate the group, and the only way to know is
// to check that it returns something other than the leader.
//
// sampleGroup falls back to the leader alone when this returns nothing, so a
// version of this that quietly finds no one still produces plausible-looking
// graphs. It did exactly that for a while: the selector was PROC_TTY_ONLY
// rather than PROC_PGRP_ONLY, so the kernel was being asked which processes are
// attached to the tty whose device number happens to equal a pgid, and answered
// "none" without an error. Nothing noticed, because the leader used to be the
// server itself and sampling it alone was very nearly right.
func TestPidsInGroupFindsEveryMemberOfTheGroup(t *testing.T) {
	// A shell with a child of its own, so the group holds more than one
	// process no matter which of them the shell decides to exec.
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	pgid := cmd.Process.Pid
	defer func() {
		_ = unix.Kill(-pgid, unix.SIGKILL)
		_, _ = cmd.Process.Wait()
	}()

	var pids []int
	waitFor(t, "the group to be populated", func() bool {
		var err error
		pids, err = pidsInGroup(pgid)
		return err == nil && len(pids) > 1
	})

	var sawLeader bool
	for _, p := range pids {
		if p == pgid {
			sawLeader = true
		}
	}
	if !sawLeader {
		t.Errorf("expected the group leader %d among %v", pgid, pids)
	}
}

// A group that does not exist is not an error worth failing a sample over, but
// it must not come back claiming members either.
func TestPidsInGroupOfAnUnusedGroupIsEmpty(t *testing.T) {
	// Pid 0 is never a real process group here, and the function rejects it
	// outright rather than asking the kernel.
	if _, err := pidsInGroup(0); err == nil {
		t.Error("expected an error for an invalid group")
	}
}
