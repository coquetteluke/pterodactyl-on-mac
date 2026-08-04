//go:build darwin

package serveruser

import (
	"bufio"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"emperror.dev/errors"
	"github.com/apex/log"
)

// macOS has no useradd. Accounts live in Directory Services and are managed
// with dscl, which is scriptable and needs no interactive input.
const dscl = "/usr/bin/dscl"

// lookup finds an existing account by name.
func lookup(name string) (Account, bool, error) {
	u, err := user.Lookup(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return Account{}, false, nil
		}
		return Account{}, false, errors.Wrap(err, "serveruser: failed to look up account")
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return Account{}, false, errors.Wrap(err, "serveruser: account has a non-numeric uid")
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return Account{}, false, errors.Wrap(err, "serveruser: account has a non-numeric gid")
	}
	return Account{Username: name, UID: uid, GID: gid}, true, nil
}

func ensure(uuid string) (Account, error) {
	name := Name(uuid)
	if acct, ok, err := lookup(name); err != nil {
		return Account{}, err
	} else if ok {
		return acct, nil
	}

	id, err := allocateID(uuid)
	if err != nil {
		return Account{}, err
	}

	// A matching group is created first so the account has a primary group of
	// its own; sharing one would defeat the isolation this exists for.
	steps := [][]string{
		{"-create", "/Groups/" + name},
		{"-create", "/Groups/" + name, "PrimaryGroupID", strconv.Itoa(id)},
		{"-create", "/Users/" + name},
		{"-create", "/Users/" + name, "UniqueID", strconv.Itoa(id)},
		{"-create", "/Users/" + name, "PrimaryGroupID", strconv.Itoa(id)},
		{"-create", "/Users/" + name, "RealName", "Pterodactyl server " + uuid},
		// No shell and no home: this account exists only to own files and run
		// the server process, and must not be usable to log in.
		{"-create", "/Users/" + name, "UserShell", "/usr/bin/false"},
		{"-create", "/Users/" + name, "NFSHomeDirectory", "/var/empty"},
		{"-create", "/Users/" + name, "IsHidden", "1"},
	}
	for _, args := range steps {
		if out, err := exec.Command(dscl, append([]string{"."}, args...)...).CombinedOutput(); err != nil {
			return Account{}, errors.Wrapf(err, "serveruser: dscl %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		}
	}

	log.WithField("account", name).WithField("uid", id).Info("created dedicated account for server")
	return Account{Username: name, UID: id, GID: id}, nil
}

// allocateID finds a free id in the reserved range, checking both users and
// groups so the two never disagree.
func allocateID(uuid string) (int, error) {
	taken, err := takenIDs()
	if err != nil {
		return 0, err
	}
	span := uidRangeEnd - uidRangeStart + 1
	start := candidateUID(uuid)
	for i := 0; i < span; i++ {
		id := uidRangeStart + ((start - uidRangeStart + i) % span)
		if !taken[id] {
			return id, nil
		}
	}
	return 0, errors.New("serveruser: no free uid available in the reserved range")
}

func takenIDs() (map[int]bool, error) {
	taken := make(map[int]bool)
	for _, path := range []struct{ node, attr string }{
		{"/Users", "UniqueID"},
		{"/Groups", "PrimaryGroupID"},
	} {
		out, err := exec.Command(dscl, ".", "-list", path.node, path.attr).Output()
		if err != nil {
			return nil, errors.Wrapf(err, "serveruser: could not enumerate %s", path.node)
		}
		s := bufio.NewScanner(strings.NewReader(string(out)))
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) < 2 {
				continue
			}
			if id, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
				taken[id] = true
			}
		}
	}
	return taken, nil
}
