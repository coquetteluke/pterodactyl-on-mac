//go:build darwin

package native

import (
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Process accounting on darwin.
//
// The Docker environment reads cgroup counters through the Docker stats API.
// macOS has no cgroups, so we ask the kernel directly via proc_info(2), the
// syscall behind libproc's proc_pidinfo(). This keeps wings free of cgo.
//
// Layouts are taken from XNU's bsd/sys/proc_info.h. Both structs are fixed
// size and have been stable for many major releases; the kernel refuses the
// call outright if the size we pass does not match what it expects, so a
// layout drift fails loudly rather than returning silent garbage.
const (
	sysProcInfo = 336 // SYS_proc_info

	callPidInfo = 2 // PROC_INFO_CALL_PIDINFO

	flavorTBSDInfo = 3 // PROC_PIDTBSDINFO  -> struct proc_bsdinfo
	flavorTaskInfo = 4 // PROC_PIDTASKINFO  -> struct proc_taskinfo

	sizeTaskInfo = 96  // sizeof(struct proc_taskinfo)
	sizeBSDInfo  = 136 // sizeof(struct proc_bsdinfo)
)

// taskInfo mirrors struct proc_taskinfo.
//
// The CPU fields are converted to nanoseconds by pidTaskInfo before being
// returned; the kernel reports them in mach absolute time units.
type taskInfo struct {
	VirtualSize   uint64
	ResidentSize  uint64
	TotalUser     uint64 // nanoseconds of user CPU time
	TotalSystem   uint64 // nanoseconds of system CPU time
	ThreadsUser   uint64
	ThreadsSystem uint64
	Policy        int32
	Faults        int32
	Pageins       int32
	CowFaults     int32
	MessagesSent  int32
	MessagesRecv  int32
	SyscallsMach  int32
	SyscallsUnix  int32
	Csw           int32
	ThreadNum     int32
	NumRunning    int32
	Priority      int32
}

// pidTaskInfo returns memory and cumulative CPU accounting for a live process.
// A process that has exited yields unix.ESRCH.
func pidTaskInfo(pid int) (*taskInfo, error) {
	var ti taskInfo
	_, _, errno := syscall.Syscall6(
		sysProcInfo,
		callPidInfo,
		uintptr(pid),
		flavorTaskInfo,
		0,
		uintptr(unsafe.Pointer(&ti)),
		sizeTaskInfo,
	)
	if errno != 0 {
		return nil, errno
	}
	ti.TotalUser = machToNanos(ti.TotalUser)
	ti.TotalSystem = machToNanos(ti.TotalSystem)
	return &ti, nil
}

var (
	timebaseOnce sync.Once
	timebaseHz   uint64
)

// machToNanos converts mach absolute time units to nanoseconds.
//
// The kernel reports task CPU time in mach ticks, not nanoseconds. On Intel
// Macs one tick happens to be exactly one nanosecond, which hides the bug; on
// Apple silicon the timebase is 24MHz, so treating ticks as nanoseconds
// under-reports CPU usage by a factor of ~41.7.
//
// hw.tbfrequency is the tick rate in Hz and is the sysctl equivalent of
// mach_timebase_info, which is only reachable through cgo.
func machToNanos(ticks uint64) uint64 {
	timebaseOnce.Do(func() {
		hz, err := unix.SysctlUint64("hw.tbfrequency")
		if err != nil || hz == 0 {
			// Fall back to treating ticks as nanoseconds, which is correct on
			// Intel and no worse than the alternative elsewhere.
			hz = 1e9
		}
		timebaseHz = hz
	})
	if timebaseHz == 1e9 {
		return ticks
	}
	// Split the conversion so that a long-running process cannot overflow:
	// ticks*1e9 alone exceeds uint64 after roughly an hour at 24MHz.
	return (ticks/timebaseHz)*1e9 + ((ticks%timebaseHz)*1e9)/timebaseHz
}

// pidStartTime returns the wall-clock time at which a process was started.
//
// This is read from the kernel rather than remembered in wings so that uptime
// stays correct for a server that wings adopted after a restart of its own.
func pidStartTime(pid int) (time.Time, error) {
	buf := make([]byte, sizeBSDInfo)
	_, _, errno := syscall.Syscall6(
		sysProcInfo,
		callPidInfo,
		uintptr(pid),
		flavorTBSDInfo,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		sizeBSDInfo,
	)
	if errno != 0 {
		return time.Time{}, errno
	}
	// pbi_start_tvsec and pbi_start_tvusec are the final two uint64 fields of
	// struct proc_bsdinfo.
	sec := *(*uint64)(unsafe.Pointer(&buf[sizeBSDInfo-16]))
	usec := *(*uint64)(unsafe.Pointer(&buf[sizeBSDInfo-8]))
	return time.Unix(int64(sec), int64(usec)*1000), nil
}

// SSTOP is the proc_bsdinfo status for a process stopped by a signal.
const sstop = 4

// processIsStopped reports whether a process is currently suspended.
//
// Used so that a resume is only ever sent to a process that actually needs it:
// signalling one that is running would be visible to it -- SIGCONT is
// deliverable and trappable -- and the whole point of the limiter is that a
// server inside its allowance is never touched.
func processIsStopped(pid int) bool {
	if pid <= 0 {
		return false
	}
	buf := make([]byte, sizeBSDInfo)
	_, _, errno := syscall.Syscall6(
		sysProcInfo,
		callPidInfo,
		uintptr(pid),
		flavorTBSDInfo,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		sizeBSDInfo,
	)
	if errno != 0 {
		return false
	}
	// pbi_status is the second uint32 of struct proc_bsdinfo.
	return *(*uint32)(unsafe.Pointer(&buf[4])) == sstop
}

// processIsAlive reports whether a pid refers to a live process. Signal 0
// performs the permission and existence checks without delivering anything.
func processIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return unix.Kill(pid, 0) == nil
}

// totalMemory returns the machine's installed physical memory in bytes.
func totalMemory() (uint64, error) {
	return unix.SysctlUint64("hw.memsize")
}

const (
	callListPids = 1 // PROC_INFO_CALL_LISTPIDS
	pgrpOnly     = 3 // PROC_PGRP_ONLY
)

// pidsInGroup returns every pid belonging to a process group.
//
// The startup command runs under a shell. A simple command line is exec'd by
// the shell and so the server is the group leader, but anything involving a
// pipeline or a wrapper script leaves the leader as a near-idle shell with the
// real server as a sibling. Accounting for the whole group keeps the Panel's
// graphs honest in both cases.
func pidsInGroup(pgid int) ([]int, error) {
	if pgid <= 0 {
		return nil, unix.EINVAL
	}

	// Ask once for the required size, then read into a buffer with room to
	// spare so a process spawned in between does not truncate the answer.
	n, _, errno := syscall.Syscall6(sysProcInfo, callListPids, pgrpOnly, uintptr(pgid), 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	count := int(n)/4 + 16
	buf := make([]int32, count)

	n, _, errno = syscall.Syscall6(
		sysProcInfo,
		callListPids,
		pgrpOnly,
		uintptr(pgid),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(count*4),
	)
	if errno != 0 {
		return nil, errno
	}

	out := make([]int, 0, int(n)/4)
	for _, p := range buf[:int(n)/4] {
		if p > 0 {
			out = append(out, int(p))
		}
	}
	return out, nil
}
