package native

import (
	"os"
	"testing"

	"github.com/pterodactyl/wings/internal/serveruser"
)

// Everything done by account signals every process a uid owns. It must never
// be done for a uid that is not a server's alone, whatever the server's
// metadata claims: root's processes are the machine, and the ones of the user
// wings runs as include wings and every other server.
func TestAccountUIDRefusesAnAccountThatIsNotTheServersAlone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account *serveruser.Account
		ok      bool
	}{
		{"no account", nil, false},
		{"root", &serveruser.Account{Username: "root", UID: 0, GID: 0}, false},
		{"a negative uid", &serveruser.Account{Username: "bad", UID: -1, GID: -1}, false},
		{"the user wings runs as", &serveruser.Account{Username: "wings", UID: os.Getuid(), GID: os.Getgid()}, false},
		{"an account of its own", &serveruser.Account{Username: "ptero-test", UID: os.Getuid() + 12345, GID: 12345}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := New("test-server", &Metadata{Account: tc.account}, nil)
			if err != nil {
				t.Fatalf("new environment: %v", err)
			}
			uid, ok := e.accountUID()
			if ok != tc.ok {
				t.Fatalf("expected ok=%t, got %t (uid %d)", tc.ok, ok, uid)
			}
			if ok && uid != tc.account.UID {
				t.Fatalf("expected uid %d, got %d", tc.account.UID, uid)
			}
			if !ok && uid != 0 {
				t.Fatalf("expected no uid alongside a refusal, got %d", uid)
			}
		})
	}
}

// Without an account none of it does anything at all.
func TestAccountSweepsAreNoOpsWithoutAnAccount(t *testing.T) {
	e, err := New("test-server", &Metadata{}, nil)
	if err != nil {
		t.Fatalf("new environment: %v", err)
	}
	if procs := e.owned(); len(procs) != 0 {
		t.Fatalf("expected no processes for a server with no account, got %v", procs)
	}
	if procs := e.strays(0); len(procs) != 0 {
		t.Fatalf("expected no strays for a server with no account, got %v", procs)
	}
	if n := e.clearAccount(); n != 0 {
		t.Fatalf("expected nothing to be killed for a server with no account, got %d", n)
	}
}
