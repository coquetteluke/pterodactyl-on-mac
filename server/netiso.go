package server

import (
	"sort"

	"emperror.dev/errors"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/internal/netiso"
	"github.com/pterodactyl/wings/internal/serveruser"
)

// ApplyNetworkIsolation rebuilds the host firewall policy from the servers this
// node currently knows about.
//
// The policy is regenerated wholesale rather than amended per server. pf loads
// an anchor atomically, so replacing the whole thing is both simpler and safer
// than tracking incremental changes: a server that was removed cannot leave a
// stale rule behind granting its old uid access.
//
// This is a no-op unless network isolation is switched on, so it is safe to
// call from anywhere the set of servers or their allocations may have changed.
func (m *Manager) ApplyNetworkIsolation() error {
	cfg := config.Get()
	if !cfg.System.NetworkIsolation.Enabled {
		return nil
	}
	if !config.UseNativeEnvironment() {
		// Docker gives each server its own network namespace already.
		return nil
	}
	if !netiso.Supported() {
		return errors.New("server: network isolation is only available on darwin")
	}
	// Rules match on the uid owning a socket, so without per-server accounts
	// every server looks identical to pf and no policy could separate them.
	if !cfg.System.User.PerServer {
		return errors.New("server: network_isolation requires system.user.per_server, since rules are matched on each server's own uid")
	}
	if !netiso.Available() {
		return errors.New("server: network isolation requires wings to run as root, since only root may load pf rules")
	}

	rules := make([]netiso.Rule, 0, m.Len())
	for _, s := range m.All() {
		acct, ok, err := serveruser.Lookup(s.ID())
		if err != nil {
			return errors.WrapIf(err, "server: could not look up the account for a server while building network rules")
		}
		if !ok {
			// The account is created when the server is first provisioned. A
			// server without one has never started, so it has nothing to
			// confine yet and will be picked up on the next rebuild.
			continue
		}
		rules = append(rules, netiso.Rule{
			UUID:     s.ID(),
			UID:      acct.UID,
			Ports:    allocatedPorts(s),
			AllowOut: cfg.System.NetworkIsolation.AllowOut,
		})
	}

	return netiso.Apply(rules)
}

// allocatedPorts returns every port the Panel has assigned to a server.
//
// Inbound traffic is accepted on these and nowhere else, so missing one would
// leave players unable to connect on that allocation. The default mapping is
// included explicitly because it is not guaranteed to appear in Mappings.
func allocatedPorts(s *Server) []int {
	alloc := s.Config().Allocations

	seen := make(map[int]struct{})
	if p := alloc.DefaultMapping.Port; p > 0 {
		seen[p] = struct{}{}
	}
	for _, ports := range alloc.Mappings {
		for _, p := range ports {
			if p > 0 {
				seen[p] = struct{}{}
			}
		}
	}

	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	// Sorted so an unchanged set of allocations always renders identically and
	// does not look like a policy change.
	sort.Ints(out)
	return out
}
