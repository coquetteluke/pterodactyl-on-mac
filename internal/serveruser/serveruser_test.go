package serveruser

import (
	"strings"
	"testing"
)

func TestName_IsDeterministicAndShort(t *testing.T) {
	const uuid = "a1b2c3d4-0000-4000-8000-000000000001"

	got := Name(uuid)
	if got != Name(uuid) {
		t.Fatal("the same server must always map to the same account name")
	}
	if want := "ptero-a1b2c3d4"; got != want {
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
	a := Name("a1b2c3d4-0000-4000-8000-000000000001")
	b := Name("b2c3d4e5-0000-4000-8000-000000000002")
	if a == b {
		t.Fatalf("distinct servers produced the same account name: %q", a)
	}
}

func TestCandidateUID_StaysInsideTheReservedRange(t *testing.T) {
	// The range matters: below it are macOS's own service accounts, above it
	// are real login users. Allocating outside it would collide with either.
	for _, uuid := range []string{
		"a1b2c3d4-0000-4000-8000-000000000001",
		"b2c3d4e5-0000-4000-8000-000000000002",
		"c3d4e5f6-0000-4000-8000-000000000003",
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
