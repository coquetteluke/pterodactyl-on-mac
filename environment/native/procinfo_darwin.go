//go:build darwin

package native

import (
	"os"
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

// bsdInfo mirrors struct proc_bsdinfo.
type bsdInfo struct {
	Flags     uint32
	Status    uint32 // SSTOP, SZOMB and friends
	XStatus   uint32
	Pid       uint32
	PPid      uint32
	UID       uint32 // effective
	GID       uint32
	RUID      uint32 // real
	RGID      uint32
	SVUID     uint32
	SVGID     uint32
	_         uint32
	Comm      [16]byte
	Name      [32]byte
	NFiles    uint32
	PGid      uint32
	PJobc     uint32
	TDev      uint32
	TPGid     uint32
	Nice      int32
	StartSec  uint64
	StartUsec uint64
}

// The kernel refuses a size it does not expect. This fails to compile if the
// struct above is any other size, so a slip in it is caught here and not as
// every call failing at runtime.
var _ [0]struct{} = [unsafe.Sizeof(bsdInfo{}) - sizeBSDInfo]struct{}{}

// Values of bsdInfo.Status, from XNU's bsd/sys/proc.h.
const (
	sstop = 4 // SSTOP: stopped by a signal
	szomb = 5 // SZOMB: exited, and waiting for its parent to collect it
)

// pidBSDInfo returns who a process is: its owner, its process group, when it
// was started and whether it is running. Unlike pidTaskInfo it still answers
// for a process that has exited and not yet been reaped.
func pidBSDInfo(pid int) (*bsdInfo, error) {
	var bi bsdInfo
	_, _, errno := syscall.Syscall6(
		sysProcInfo,
		callPidInfo,
		uintptr(pid),
		flavorTBSDInfo,
		0,
		uintptr(unsafe.Pointer(&bi)),
		sizeBSDInfo,
	)
	if errno != 0 {
		return nil, errno
	}
	return &bi, nil
}

// pidStartTime returns the wall-clock time at which a process was started.
//
// This is read from the kernel rather than remembered in wings so that uptime
// stays correct for a server that wings adopted after a restart of its own.
func pidStartTime(pid int) (time.Time, error) {
	bi, err := pidBSDInfo(pid)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(bi.StartSec), int64(bi.StartUsec)*1000), nil
}

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
	bi, err := pidBSDInfo(pid)
	return err == nil && bi.Status == sstop
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

	// What a listing is narrowed to. Each takes the value to match.
	pgrpOnly = 2 // PROC_PGRP_ONLY: the members of a process group
	uidOnly  = 4 // PROC_UID_ONLY: processes whose effective uid is the one given
	ruidOnly = 5 // PROC_RUID_ONLY: the same, by real uid
)

// listPids returns the pids the kernel lists for a selector and the value it
// is to match. Processes that have exited and not been reaped are included.
func listPids(kind, value int) ([]int, error) {
	// Ask once for the required size, then read into a buffer with room to
	// spare so a process spawned in between does not truncate the answer.
	n, _, errno := syscall.Syscall6(sysProcInfo, callListPids, uintptr(kind), uintptr(value), 0, 0, 0)
	if errno != 0 {
		return nil, errno
	}
	count := int(n)/4 + 16
	buf := make([]int32, count)

	n, _, errno = syscall.Syscall6(
		sysProcInfo,
		callListPids,
		uintptr(kind),
		uintptr(value),
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
	return listPids(pgrpOnly, pgid)
}

// pidsOwnedBy returns every pid running as a uid, whichever process group or
// session it is in.
//
// Both the effective and the real uid are asked for. They only differ for a
// program that is setuid to someone else, which then runs with that owner's
// effective uid and the real uid of whoever started it, and is still theirs.
func pidsOwnedBy(uid int) ([]int, error) {
	if uid < 0 {
		return nil, unix.EINVAL
	}
	pids, err := listPids(uidOnly, uid)
	if err != nil {
		return nil, err
	}
	byReal, err := listPids(ruidOnly, uid)
	if err != nil {
		return nil, err
	}
	seen := make(map[int]struct{}, len(pids))
	for _, p := range pids {
		seen[p] = struct{}{}
	}
	for _, p := range byReal {
		if _, ok := seen[p]; !ok {
			pids = append(pids, p)
		}
	}
	return pids, nil
}

// processesOwnedBy returns the live processes running as a uid.
//
// This is what signals are sent on the strength of, so each pid the kernel
// listed is looked at again before it is returned: one that exited in between
// is dropped, as is anything that is not running as the uid after all. A
// process that has exited and is only waiting to be reaped is left out too. It
// cannot be signalled, and counting it would have a caller wait on something
// that is already dead.
//
// Root's processes are never returned, and neither is wings itself.
func processesOwnedBy(uid int) ([]ownedProcess, error) {
	if uid <= 0 {
		return nil, unix.EINVAL
	}
	pids, err := pidsOwnedBy(uid)
	if err != nil {
		return nil, err
	}

	self := os.Getpid()
	out := make([]ownedProcess, 0, len(pids))
	for _, pid := range pids {
		if pid <= 1 || pid == self {
			continue
		}
		bi, err := pidBSDInfo(pid)
		if err != nil || bi.Status == szomb {
			continue
		}
		if bi.UID != uint32(uid) && bi.RUID != uint32(uid) {
			continue
		}
		out = append(out, ownedProcess{pid: pid, pgid: int(bi.PGid), stopped: bi.Status == sstop})
	}
	return out, nil
}
