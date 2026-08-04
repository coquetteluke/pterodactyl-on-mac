// Package serveruser provisions a dedicated operating system account per
// server, so that servers are isolated from each other by file permissions.
//
// Upstream Wings gets this isolation from Docker: every server runs in its own
// container and cannot see another server's files. Without containers all
// servers would otherwise run as the same user, which means any one of them can
// read and modify every other server's data, and can read Wings' own config.yml
// -- which holds the node token that authenticates to the Panel.
//
// Giving each server its own uid restores that boundary the pre-container way,
// with ordinary unix permissions. It does not restore resource limits: there is
// still no cgroup equivalent on darwin, so a server can starve its neighbours
// even when it cannot read their files.
//
// This requires Wings to run as root, since only root may change a process's
// user. Wings drops to the per-server account when it spawns the server.
package serveruser

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"

	"emperror.dev/errors"
)

// Account describes the operating system account a server runs as.
type Account struct {
	Username string
	UID      int
	GID      int
}

// ErrNotSupported is returned on platforms where per-server accounts have not
// been implemented.
var ErrNotSupported = errors.Sentinel("serveruser: per-server accounts are not supported on this platform")

// ErrNotRoot is returned when per-server accounts are requested but Wings is
// not running with the privileges needed to create accounts or change user.
var ErrNotRoot = errors.Sentinel("serveruser: per-server accounts require wings to run as root")

const (
	// uidRangeStart and uidRangeEnd bound the range accounts are allocated
	// from. macOS keeps its own service accounts below 500 and real login users
	// at 501 and up, so this sits in the gap that is conventionally used for
	// additional system daemons.
	uidRangeStart = 700
	uidRangeEnd   = 999
)

// Name returns the deterministic account name for a server.
//
// Deriving it from the server UUID means the same server always maps to the
// same account without Wings having to persist that mapping anywhere, and the
// account can be found again after a reinstall. macOS shortens names above 31
// characters in some contexts, so only the first segment of the UUID is used.
func Name(uuid string) string {
	short := uuid
	if i := strings.IndexByte(short, '-'); i > 0 {
		short = short[:i]
	}
	if len(short) > 8 {
		short = short[:8]
	}
	return "ptero-" + short
}

// candidateUID gives a starting point for allocation that is spread across the
// range rather than always beginning at the bottom, which keeps servers created
// in a batch from all probing the same slots.
func candidateUID(uuid string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(uuid))
	span := uidRangeEnd - uidRangeStart + 1
	return uidRangeStart + int(h.Sum32()%uint32(span))
}

// RunningAsRoot reports whether Wings has the privileges this package needs.
func RunningAsRoot() bool {
	return os.Geteuid() == 0
}

// Ensure returns the account for a server, creating it if it does not exist.
func Ensure(uuid string) (Account, error) {
	if !RunningAsRoot() {
		return Account{}, ErrNotRoot
	}
	return ensure(uuid)
}

// Lookup returns the account for a server if it already exists. It does not
// require root, so callers that only need the ids can use it.
func Lookup(uuid string) (Account, bool, error) {
	return lookup(Name(uuid))
}

func (a Account) String() string {
	return fmt.Sprintf("%s (uid=%d gid=%d)", a.Username, a.UID, a.GID)
}
