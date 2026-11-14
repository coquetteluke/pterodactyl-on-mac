package netiso

import (
	"net"
	"strings"
	"testing"
)

func TestRulesetConfinesPrivateRanges(t *testing.T) {
	got, err := Ruleset([]Rule{{UUID: "abc", UID: 700, Ports: []int{25565}}})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	for _, cidr := range privateRanges {
		want := "block drop out quick from any to " + cidr + " user 700"
		if !strings.Contains(got, want) {
			t.Errorf("missing block for %s\n%s", cidr, got)
		}
	}
}

// Reaching the LAN does not take a unicast address on it. Multicast and
// broadcast get to every host at once, and a network that translates between
// IPv6 and IPv4 gives each private IPv4 address a second, IPv6 spelling.
func TestRulesetConfinesTheOtherWaysOntoTheLAN(t *testing.T) {
	got, err := Ruleset([]Rule{{UUID: "abc", UID: 700, Ports: []int{25565}}})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	for _, cidr := range []string{
		"224.0.0.0/4",         // mDNS and SSDP live here
		"240.0.0.0/4",         // 255.255.255.255
		"ff00::/8",            // IPv6 multicast
		"64:ff9b::7f00:0/104", // 127.0.0.0/8 behind NAT64
		"64:ff9b::a00:0/104",  // 10.0.0.0/8
		"64:ff9b::ac10:0/108", // 172.16.0.0/12
		"64:ff9b::c0a8:0/112", // 192.168.0.0/16
		"64:ff9b::a9fe:0/112", // 169.254.0.0/16
		"64:ff9b::6440:0/106", // 100.64.0.0/10
		"2002::/16",           // 6to4
	} {
		want := "block drop out quick from any to " + cidr + " user 700"
		if !strings.Contains(got, want) {
			t.Errorf("missing block for %s\n%s", cidr, got)
		}
	}
	// Proxy tools on macOS route the public internet through this range.
	if strings.Contains(got, "198.18.") {
		t.Errorf("198.18.0.0/15 must stay reachable\n%s", got)
	}
}

// Every range has to be something pf and the net package agree is a network.
func TestPrivateRangesAreValidNetworks(t *testing.T) {
	seen := make(map[string]bool)
	for _, cidr := range privateRanges {
		ip, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Errorf("%s: %v", cidr, err)
			continue
		}
		if !ip.Equal(n.IP) {
			t.Errorf("%s has bits set beyond its prefix; the network is %s", cidr, n)
		}
		if seen[cidr] {
			t.Errorf("%s is listed twice", cidr)
		}
		seen[cidr] = true
	}
}

// A host's public and global v6 addresses fall outside the private ranges, and
// would otherwise reach the Wings API and SFTP on the host itself.
func TestRulesetConfinesHostAddresses(t *testing.T) {
	got, err := Ruleset([]Rule{{UUID: "abc", UID: 700, Ports: []int{25565}}})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	if !strings.Contains(got, "block drop out quick from any to self user 700") {
		t.Errorf("missing block for the host's own addresses\n%s", got)
	}
}

func TestRulesetAcceptsOnlyAllocatedPorts(t *testing.T) {
	got, err := Ruleset([]Rule{{UUID: "abc", UID: 700, Ports: []int{25565, 25575}}})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	want := "pass in quick proto { tcp udp } from any to any port { 25565 25575 } user 700 keep state"
	if !strings.Contains(got, want) {
		t.Errorf("expected inbound rule %q in:\n%s", want, got)
	}
	// Exactly one inbound pass, so a second allocation cannot be smuggled in.
	if n := strings.Count(got, "pass in quick"); n != 1 {
		t.Errorf("expected 1 inbound rule, got %d:\n%s", n, got)
	}
}

// The allow exceptions are useless unless pf reaches them before the drop, so
// assert the order rather than merely the presence of both.
func TestAllowOutPrecedesPrivateBlock(t *testing.T) {
	got, err := Ruleset([]Rule{{
		UUID:     "abc",
		UID:      700,
		Ports:    []int{25565},
		AllowOut: []string{"192.168.1.50"},
	}})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	allow := strings.Index(got, "pass out quick proto { tcp udp } from any to 192.168.1.50 user 700")
	block := strings.Index(got, "block drop out quick from any to 192.168.0.0/16 user 700")
	if allow == -1 || block == -1 {
		t.Fatalf("expected both an allow and a block:\n%s", got)
	}
	if allow > block {
		t.Errorf("allow_out is evaluated after the private-range block, so it has no effect:\n%s", got)
	}
}

func TestDNSIsAlwaysAllowed(t *testing.T) {
	got, err := Ruleset([]Rule{{UUID: "abc", UID: 700}})
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	if !strings.Contains(got, "pass out quick proto { tcp udp } from any to any port 53 user 700 keep state") {
		t.Errorf("expected DNS to be permitted:\n%s", got)
	}
}

// Without a dedicated uid every rule would match every server, so a policy
// that cannot discriminate must be refused rather than generated.
func TestRejectsServerWithoutDedicatedUID(t *testing.T) {
	if _, err := Ruleset([]Rule{{UUID: "abc", UID: 0, Ports: []int{25565}}}); err == nil {
		t.Fatal("expected an error for a server with no dedicated uid")
	}
}

func TestRejectsSharedUID(t *testing.T) {
	_, err := Ruleset([]Rule{
		{UUID: "abc", UID: 700, Ports: []int{25565}},
		{UUID: "def", UID: 700, Ports: []int{25566}},
	})
	if err == nil {
		t.Fatal("expected an error when two servers share a uid")
	}
	if !strings.Contains(err.Error(), "share uid") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A hostname is resolved once at load time, so it silently stops matching when
// the address behind it moves. For a security control that is worse than a
// refusal.
func TestRejectsHostnameInAllowOut(t *testing.T) {
	if _, err := Ruleset([]Rule{{UUID: "abc", UID: 700, AllowOut: []string{"db.example.com"}}}); err == nil {
		t.Fatal("expected an error for a hostname in allow_out")
	}
}

func TestAcceptsCIDRInAllowOut(t *testing.T) {
	if _, err := Ruleset([]Rule{{UUID: "abc", UID: 700, AllowOut: []string{"192.168.1.0/24"}}}); err != nil {
		t.Fatalf("expected a CIDR to be accepted: %v", err)
	}
}

func TestRejectsOutOfRangePort(t *testing.T) {
	if _, err := Ruleset([]Rule{{UUID: "abc", UID: 700, Ports: []int{70000}}}); err == nil {
		t.Fatal("expected an error for an out-of-range port")
	}
}

// Ordering by uid keeps the generated file stable, so an unchanged policy does
// not produce a spurious reload.
func TestRulesetIsDeterministic(t *testing.T) {
	a := []Rule{{UUID: "a", UID: 701, Ports: []int{2}}, {UUID: "b", UID: 700, Ports: []int{1}}}
	b := []Rule{{UUID: "b", UID: 700, Ports: []int{1}}, {UUID: "a", UID: 701, Ports: []int{2}}}
	ra, err := Ruleset(a)
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	rb, err := Ruleset(b)
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	if ra != rb {
		t.Errorf("ruleset depends on input order:\n%s\n---\n%s", ra, rb)
	}
}

func TestEmptyRulesetIsValid(t *testing.T) {
	got, err := Ruleset(nil)
	if err != nil {
		t.Fatalf("Ruleset: %v", err)
	}
	if strings.Contains(got, "pass") || strings.Contains(got, "block") {
		t.Errorf("expected no rules for no servers:\n%s", got)
	}
}
