package serveruser

import (
	"strings"
	"testing"
)

func TestName_IsDeterministicAndShort(t *testing.T) {
	const uuid = "ca354ffe-e0ba-4374-b560-cc15a162d697"

	got := Name(uuid)
	if got != Name(uuid) {
		t.Fatal("the same server must always map to the same account name")
	}
	if want := "ptero-ca354ffe"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Directory Services truncates long names in some contexts, so the account
	// name has to stay comfortably short.
	if len(got) > 31 {
		t.Fatalf("account name %q is too long at %d characters", got, len(got))
	}
	if strings.ContainsAny(got, " \t/:") {
		t.Fatalf("account name %q contains characters that are unsafe in a dscl path", got)
	}
}

func TestName_DifferentServersDoNotCollide(t *testing.T) {
	a := Name("ca354ffe-e0ba-4374-b560-cc15a162d697")
	b := Name("2d1a8abd-5bfb-4eb3-a5b8-787e14b8896a")
	if a == b {
		t.Fatalf("distinct servers produced the same account name: %q", a)
	}
}

func TestCandidateUID_StaysInsideTheReservedRange(t *testing.T) {
	// The range matters: below it are macOS's own service accounts, above it
	// are real login users. Allocating outside it would collide with either.
	for _, uuid := range []string{
		"ca354ffe-e0ba-4374-b560-cc15a162d697",
		"2d1a8abd-5bfb-4eb3-a5b8-787e14b8896a",
		"0629d6e1-c5c8-4d4a-899d-c777798f2671",
		"",
		"not-a-uuid",
	} {
		id := candidateUID(uuid)
		if id < uidRangeStart || id > uidRangeEnd {
			t.Fatalf("candidate uid %d for %q is outside [%d,%d]", id, uuid, uidRangeStart, uidRangeEnd)
		}
	}
}

func TestCandidateUID_SpreadsAcrossTheRange(t *testing.T) {
	// A hash that collapsed every server onto one starting point would make
	// allocation quadratic and defeat the probe order.
	seen := make(map[int]bool)
	for _, uuid := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen[candidateUID(uuid)] = true
	}
	if len(seen) < 4 {
		t.Fatalf("expected candidates to spread out, got only %d distinct values", len(seen))
	}
}
