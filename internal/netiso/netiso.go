// Package netiso confines what the network a server can reach.
//
// On Linux this is what a container network namespace does: a server gets its
// own stack, and reaching the host or a sibling means routing that the daemon
// never sets up. macOS has no namespaces, so every server shares the host's
// single network stack and can, by default, open a connection to anything the
// host can reach -- the Panel, the node's own API, a NAS, the router's admin
// page, or another server's RCON port.
//
// What macOS does have is pf, and pf can match on the uid that owns a socket.
// Combined with the per-server accounts in internal/serveruser that gives a
// per-server policy enforced in the kernel, keyed on identity rather than on
// address. It is not a network namespace: a server still shares the host's
// addresses and can still see its own traffic. What it buys is that a
// compromised server cannot pivot -- it cannot reach the Panel it is registered
// with, the machine it runs on, or anything else on the LAN.
//
// This is only meaningful alongside per-server accounts. Without them every
// server runs as the same uid, so a per-uid rule cannot tell them apart, and
// Enabled reports false.
package netiso

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Rule is the policy for a single server.
type Rule struct {
	// UUID identifies the server, used only to label the generated rules.
	UUID string

	// UID is the account the server runs as. All matching is keyed on this, so
	// a rule with no uid cannot be enforced and is rejected.
	UID int

	// Ports are the allocations the server listens on. Inbound traffic is
	// accepted on these and nowhere else.
	Ports []int

	// AllowOut lists destinations the server may reach even though they fall
	// inside a blocked private range. A server that stores data in a database
	// on the LAN needs its address listed here or it will not start correctly.
	AllowOut []string
}

// privateRanges are the destinations a confined server is kept away from.
//
// This is the whole point of the package: these cover the Panel, Wings' own
// API, the machine's loopback services, every other host on the LAN, and the
// link-local range that carries mDNS and, on macOS, the cloud identity
// services. Public addresses are deliberately left alone, because a game
// server has to reach the session servers to verify players.
var privateRanges = []string{
	"127.0.0.0/8",     // loopback: Wings' own API, MySQL, anything bound locally
	"10.0.0.0/8",      // RFC1918
	"172.16.0.0/12",   // RFC1918
	"192.168.0.0/16",  // RFC1918
	"169.254.0.0/16",  // link-local
	"100.64.0.0/10",   // RFC6598, carrier NAT and Tailscale
	"::1/128",         // loopback, v6
	"fc00::/7",        // unique local, v6
	"fe80::/10",       // link-local, v6
}

// dnsPorts is allowed outbound regardless of destination. Resolvers usually
// live on the LAN or on the router, so blocking private ranges without this
// exception leaves a server unable to resolve anything at all.
var dnsPorts = []int{53}

// Validate reports whether a rule can be enforced.
func (r Rule) Validate() error {
	if r.UID <= 0 {
		return fmt.Errorf("netiso: server %s has no dedicated uid; network isolation requires per-server accounts", r.UUID)
	}
	for _, p := range r.Ports {
		if p <= 0 || p > 65535 {
			return fmt.Errorf("netiso: server %s has out-of-range port %d", r.UUID, p)
		}
	}
	for _, d := range r.AllowOut {
		if !validDestination(d) {
			return fmt.Errorf("netiso: server %s has an allow_out entry that is not an address or CIDR: %q", r.UUID, d)
		}
	}
	return nil
}

// validDestination accepts a bare address or a CIDR block. Hostnames are
// rejected on purpose: pf resolves names once at load time, so a rule written
// against a name silently stops matching when the address behind it changes,
// which for a security control is worse than refusing it outright.
func validDestination(s string) bool {
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	return net.ParseIP(s) != nil
}

// Ruleset renders the pf rules for a set of servers.
//
// It is pure text generation so the policy can be tested without touching the
// kernel or needing root.
//
// pf evaluates every rule and the last match wins, except where a rule is
// marked quick, which stops evaluation immediately. Every rule here is quick,
// so the order below is the order decisions are made in:
//
//  1. accept inbound on the server's own allocations
//  2. allow DNS out
//  3. allow the operator's explicit exceptions
//  4. drop everything aimed at a private range
//
// Anything not matched falls through to the surrounding ruleset, which leaves
// the public internet reachable. That ordering matters: the allow exceptions
// have to be evaluated before the private-range drop, or listing a LAN
// database would have no effect.
func Ruleset(rules []Rule) (string, error) {
	ordered := append([]Rule(nil), rules...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].UID < ordered[j].UID })

	var b strings.Builder
	b.WriteString("# Generated by wings. Do not edit; regenerated whenever a server changes.\n")

	seen := make(map[int]string, len(ordered))
	for _, r := range ordered {
		if err := r.Validate(); err != nil {
			return "", err
		}
		// Two servers sharing a uid would silently share a policy, which would
		// look like isolation without being any.
		if prev, ok := seen[r.UID]; ok {
			return "", fmt.Errorf("netiso: servers %s and %s share uid %d, which cannot be isolated from each other", prev, r.UUID, r.UID)
		}
		seen[r.UID] = r.UUID

		fmt.Fprintf(&b, "\n# server %s (uid %d)\n", r.UUID, r.UID)

		if len(r.Ports) > 0 {
			fmt.Fprintf(&b, "pass in quick proto { tcp udp } from any to any port %s user %d keep state\n",
				portList(r.Ports), r.UID)
		}
		fmt.Fprintf(&b, "pass out quick proto { tcp udp } from any to any port %s user %d keep state\n",
			portList(dnsPorts), r.UID)

		for _, d := range r.AllowOut {
			fmt.Fprintf(&b, "pass out quick proto { tcp udp } from any to %s user %d keep state\n", d, r.UID)
		}

		for _, cidr := range privateRanges {
			fmt.Fprintf(&b, "block drop out quick from any to %s user %d\n", cidr, r.UID)
		}
	}

	return b.String(), nil
}

// portList renders ports as a pf list, collapsing a single port to a bare
// number since pf rejects a one-element brace list in some positions.
func portList(ports []int) string {
	if len(ports) == 1 {
		return strconv.Itoa(ports[0])
	}
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return "{ " + strings.Join(parts, " ") + " }"
}
