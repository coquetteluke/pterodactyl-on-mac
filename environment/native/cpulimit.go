package native

import (
	"context"
	"time"

	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/config"
)

// CPU limiting on darwin.
//
// There is no scheduler quota to attach a process to. Of the mechanisms macOS
// does offer, none work as a cap: renice is one-way for an unprivileged
// process (you can lower a priority but not restore it), and the background QoS
// tier throttles I/O and timers while leaving compute untouched -- a burner
// process still sits at 99.7% of a core after taskpolicy -b.
//
// That leaves stopping and continuing the process, which does cap it, and does
// so accurately: measured within ~3% of target. The cost is that a throttled
// process is frozen in bursts, and for a game server those bursts land inside
// its tick budget.
//
// So the loop below only ever throttles a server that is over its limit. A
// server inside its allowance is never signalled, never stalls, and pays
// nothing beyond the sampling. Idle capacity is left usable rather than
// reserved, which is the behaviour you want on a machine you own -- a cgroup
// quota would idle the CPU instead of lending it out.

const (
	// cpuControlWindow is how often usage is measured and, if necessary,
	// corrected. It also bounds a single stall, since a process is never
	// stopped for longer than one window.
	//
	// 20ms measured well: a 50%-limited process settled at 47.1% with stalls of
	// 10ms, against a Minecraft tick budget of 50ms. Longer windows track the
	// target more tightly but stall for longer -- 100ms windows produced 50ms
	// stalls, a whole tick.
	cpuControlWindow = 20 * time.Millisecond

	// cpuHeadroom is how far over its limit a server may drift before being
	// corrected. Without it, sampling noise around the threshold would stop a
	// server that is essentially compliant.
	cpuHeadroom = 1.05

	// cpuAverageTau is the time constant of the smoothing applied to usage
	// before it is acted on.
	//
	// A single 20ms window is far too noisy to judge a process by: one fork and
	// exec inside it reads as several hundred percent, and a server that merely
	// spawns a helper would be throttled for it. Decisions are therefore made on
	// a smoothed average over roughly this period, while corrections are still
	// applied at the shorter window so that throttling stays responsive.
	cpuAverageTau = 250 * time.Millisecond
)

// enforceCPULimit throttles a server that exceeds its configured CPU limit,
// returning when the context is cancelled or the process exits. It always
// leaves the process running.
func (e *Environment) enforceCPULimit(ctx context.Context) {
	if !config.Get().System.EnforceCpuLimit {
		return
	}
	limit := float64(e.Configuration.Limits().CpuLimit)
	if limit <= 0 {
		// Unlimited: nothing to enforce, and no reason to burn a timer.
		return
	}

	// Do not check for a live process here. Attach runs *before* the server is
	// spawned, so at this point there is usually no pid at all; returning early
	// would silently disable enforcement for every server wings starts itself,
	// leaving it to apply only to adopted ones.
	// On the way out, only lift a stop that is actually in place.
	defer func() {
		if pid := e.currentPid(); processIsStopped(pid) {
			e.resume(pid)
		}
	}()

	e.log().WithField("cpu_limit", limit).Debug("cpu limit enforcement active")

	// alpha weights each new sample into the running average.
	alpha := float64(cpuControlWindow) / float64(cpuAverageTau)
	if alpha > 1 {
		alpha = 1
	}

	var (
		lastCPU  map[int]uint64
		lastTime time.Time
		started  bool
		average  float64
		haveAvg  bool
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cpuControlWindow):
		}

		pid := e.currentPid()
		if !processIsAlive(pid) {
			// Either the server has not started yet, or it has gone. Either way
			// there is nothing to measure; reset so the next process is not
			// judged on its predecessor's counters.
			lastTime = time.Time{}
			continue
		}

		if !started {
			started = true
			// If a previous wings was killed outright mid-stall, this group
			// could still be stopped, and nothing else will ever resume it.
			// Only signal when that is actually the case: SIGCONT is
			// deliverable and trappable, so sending it to a running server
			// would break the promise that a compliant one is never touched.
			if processIsStopped(pid) {
				e.log().WithField("pid", pid).Warn("server was left stopped by a previous limiter; resuming it")
				e.resume(pid)
			}
		}

		_, cpuNow, err := e.sampleGroup(pid)
		if err != nil {
			continue
		}
		now := time.Now()

		// First sample: establish a baseline rather than acting on nonsense.
		if lastTime.IsZero() {
			lastCPU, lastTime = cpuNow, now
			continue
		}

		delta := cpuDelta(lastCPU, cpuNow)
		elapsed := now.Sub(lastTime)
		lastCPU, lastTime = cpuNow, now
		if elapsed <= 0 {
			continue
		}

		// 100.0 is one fully saturated core, the same scale the Panel uses.
		usage := float64(delta) / float64(elapsed.Nanoseconds()) * 100

		// Decide on a smoothed figure, not a single window. One fork and exec
		// inside a 20ms window reads as several hundred percent, and acting on
		// that would throttle a server for spawning a helper process.
		if !haveAvg {
			average, haveAvg = usage, true
		} else {
			average += alpha * (usage - average)
		}

		if average <= limit*cpuHeadroom {
			// Inside its allowance. This is the path that must never signal:
			// a well-behaved server has to be completely untouched, or the
			// cure is worse than the disease.
			continue
		}

		// Stop for the share of the next window that brings usage back to the
		// limit. At twice the limit that is half the window; the further over
		// it is, the longer the stall, bounded by one window.
		stop := time.Duration(float64(cpuControlWindow) * (1 - limit/average))
		if stop > cpuControlWindow {
			stop = cpuControlWindow
		}
		e.pause(pid, stop)
	}
}

// resume lifts a stop from a server's process group.
func (e *Environment) resume(pid int) {
	if pid <= 0 {
		return
	}
	if err := unix.Kill(-pid, unix.SIGCONT); err != nil {
		_ = unix.Kill(pid, unix.SIGCONT)
	}
}

// pause stops a server's process group for d, then resumes it.
//
// The whole group is signalled so that a startup command wrapped in a shell
// throttles the actual server rather than only its wrapper.
func (e *Environment) pause(pid int, d time.Duration) {
	if d <= 0 || pid <= 0 {
		return
	}
	if err := unix.Kill(-pid, unix.SIGSTOP); err != nil {
		if err := unix.Kill(pid, unix.SIGSTOP); err != nil {
			return
		}
	}
	time.Sleep(d)
	e.resume(pid)
}
