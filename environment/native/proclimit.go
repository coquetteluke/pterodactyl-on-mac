package native

import (
	"fmt"

	"github.com/pterodactyl/wings/config"
)

// applyProcessLimit caps how many processes a server may have running.
//
// This is the one resource limit on darwin that the kernel enforces outright
// rather than Wings supervising after the fact. RLIMIT_NPROC makes fork(2)
// fail once the account is at its limit, so a fork bomb inside a server stops
// at its own ceiling instead of exhausting the machine's process table and
// taking every other server down with it.
//
// It reuses docker.container_pid_limit, which is the same knob the Docker
// environment passes to the container, so a node behaves the same either way.
//
// # Why this needs per-server accounts
//
// RLIMIT_NPROC is counted per uid, across every process that uid owns, not per
// process tree. Without a dedicated account every server shares the uid Wings
// runs as, so the limit would be consumed by all of them together, plus Wings
// itself and any shell the operator has open. The first server to start would
// effectively set a budget for the whole machine. So this only applies when the
// server has a uid to itself, and is skipped otherwise.
func (e *Environment) applyProcessLimit(startup string) string {
	limit := config.Get().Docker.ContainerPidLimit
	if limit <= 0 {
		return startup
	}

	e.mu.RLock()
	account := e.meta.Account
	e.mu.RUnlock()
	if account == nil {
		return startup
	}

	// Fail the boot rather than run on: a limit that was silently not applied
	// looks identical to one that was, which is the failure mode worth avoiding
	// in anything that exists to contain a hostile server.
	return fmt.Sprintf(
		"ulimit -u %d || { echo 'wings: failed to apply the process limit' >&2; exit 1; }\n%s",
		limit, startup,
	)
}
