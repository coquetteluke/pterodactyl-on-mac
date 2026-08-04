package native

import (
	"context"
	"math"
	"time"

	"emperror.dev/errors"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/environment"
)

// statsPollInterval matches the roughly one-second cadence the Docker stats
// stream delivers, so the Panel's graphs scroll at the same rate on either
// environment.
const statsPollInterval = time.Second

// pollResources samples the server's resource usage and publishes it until the
// context is canceled or the process exits.
//
// The Docker environment consumes a stats stream backed by cgroup counters.
// There are no cgroups here, so usage is sampled from the kernel's per-process
// accounting and CPU is differentiated between samples by hand.
func (e *Environment) pollResources(ctx context.Context) error {
	if e.st.Load() == environment.ProcessOfflineState {
		return errors.New("cannot enable resource polling on a stopped server")
	}

	e.log().Debug("starting resource polling for process")
	defer e.log().Debug("stopped resource polling for process")

	var (
		lastCPU  uint64
		lastTime time.Time
		// over counts consecutive samples above the memory limit. Acting on a
		// single sample would kill a server for a momentary spike, which the
		// allocator may hand straight back.
		over int
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(statsPollInterval):
		}

		if e.st.Load() == environment.ProcessOfflineState {
			e.log().Debug("process in offline state while resource polling is still active; stopping poll")
			return nil
		}

		pid := e.currentPid()
		if !processIsAlive(pid) {
			return nil
		}

		resident, cpuNS, err := e.sampleGroup(pid)
		if err != nil {
			// The process exiting mid-sample is entirely normal; anything else
			// is worth surfacing once rather than every second.
			if !processIsAlive(pid) {
				return nil
			}
			e.log().WithField("error", err).Debug("failed to sample process resources")
			continue
		}

		now := time.Now()
		var cpuAbsolute float64
		if !lastTime.IsZero() && cpuNS >= lastCPU {
			elapsed := now.Sub(lastTime).Nanoseconds()
			if elapsed > 0 {
				// 100.0 represents one fully saturated core, which is the same
				// scale the Docker environment reports on.
				cpuAbsolute = float64(cpuNS-lastCPU) / float64(elapsed) * 100
				cpuAbsolute = math.Round(cpuAbsolute*1000) / 1000
			}
		}
		lastCPU, lastTime = cpuNS, now

		uptime, err := e.Uptime(ctx)
		if err != nil {
			uptime = 0
		}

		limit := e.memoryLimit()
		e.Events().Publish(environment.ResourceEvent, environment.Stats{
			Uptime:      uptime,
			Memory:      resident,
			MemoryLimit: limit,
			CpuAbsolute: cpuAbsolute,
			// Per-process network accounting is not available on darwin
			// without a packet filter or a privileged helper, so these stay at
			// zero rather than reporting a number that is not true.
			Network: environment.NetworkStats{},
		})

		if over = e.checkMemoryLimit(resident, limit, over); over >= memoryGraceSamples {
			e.killForMemory(resident, limit)
			return nil
		}
	}
}

// memoryGraceSamples is how many consecutive over-limit samples are required
// before a server is killed. At the one-second poll interval this rides out
// brief spikes -- a JVM briefly touching its ceiling before a collection, for
// instance -- while still reacting within a few seconds to a server that is
// genuinely running away.
const memoryGraceSamples = 5

// checkMemoryLimit returns the updated count of consecutive over-limit samples.
func (e *Environment) checkMemoryLimit(resident, limit uint64, over int) int {
	if !config.Get().System.EnforceMemoryLimit {
		return 0
	}
	// A server with no configured limit is bounded only by the machine, and
	// memoryLimit reports total RAM for it; killing at that point would be
	// killing for using the memory it was allowed.
	if e.Configuration.Limits().MemoryLimit <= 0 || limit == 0 {
		return 0
	}
	if resident <= limit {
		return 0
	}
	return over + 1
}

// killForMemory terminates a server that has stayed above its memory limit and
// records it as an out-of-memory kill, which is what the Panel shows for a
// container the kernel killed for the same reason.
func (e *Environment) killForMemory(resident, limit uint64) {
	e.log().
		WithField("memory_bytes", resident).
		WithField("memory_limit_bytes", limit).
		Warn("server exceeded its memory limit; terminating")

	e.mu.Lock()
	e.oomKilled = true
	e.mu.Unlock()

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		e.log().WithField("error", err).Error("failed to terminate server that exceeded its memory limit")
	}
}

// sampleGroup totals resident memory and cumulative CPU time across every
// process in the server's process group.
func (e *Environment) sampleGroup(pid int) (resident uint64, cpuNS uint64, err error) {
	pids, lerr := pidsInGroup(pid)
	if lerr != nil || len(pids) == 0 {
		// Fall back to the group leader alone.
		pids = []int{pid}
	}

	var sampled int
	for _, p := range pids {
		ti, terr := pidTaskInfo(p)
		if terr != nil {
			// A process in the group exiting between the listing and the
			// sample is expected; skip it.
			continue
		}
		resident += ti.ResidentSize
		cpuNS += ti.TotalUser + ti.TotalSystem
		sampled++
	}

	if sampled == 0 {
		return 0, 0, errors.New("environment/native: no processes in group could be sampled")
	}
	return resident, cpuNS, nil
}

// memoryLimit reports the memory ceiling for this server, including the same
// overhead allowance Docker applies to a container so that the two environments
// kill at the same threshold.
//
// A server with no configured limit reports the machine's total memory, which
// is its real ceiling; enforcement skips those rather than killing a server for
// using memory it was allowed.
func (e *Environment) memoryLimit() uint64 {
	if l := e.Configuration.Limits().BoundedMemoryLimit(); l > 0 {
		return uint64(l)
	}
	if total, err := totalMemory(); err == nil {
		return total
	}
	return 0
}
