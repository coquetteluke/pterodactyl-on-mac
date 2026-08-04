package native

import (
	"os"
	"path/filepath"

	"emperror.dev/errors"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/internal/sandbox"
)

// sandboxProfileName is the file the generated profile is written to inside a
// server's runtime directory.
const sandboxProfileName = "sandbox.sb"

// sandboxCommand returns the argv to spawn a server with, wrapping it in
// sandbox-exec when filesystem confinement is switched on.
//
// The profile is regenerated on every boot rather than cached, so that a
// change to the configured denials takes effect on the next restart instead of
// whenever the file happens to be rewritten.
func (e *Environment) sandboxCommand(dir, startup string) ([]string, error) {
	plain := []string{"/bin/sh", "-c", startup}

	cfg := config.Get()
	if !cfg.System.Sandbox.Enabled {
		return plain, nil
	}
	if !sandbox.Supported() {
		return nil, errors.New("environment/native: filesystem sandboxing is only available on darwin")
	}

	body, err := sandbox.Generate(sandbox.Profile{
		ServerDir: dir,
		WingsRoot: cfg.System.RootDirectory,
		Deny:      cfg.System.Sandbox.Deny,
	})
	if err != nil {
		return nil, errors.WrapIf(err, "environment/native: failed to build the sandbox profile")
	}

	// Written only so an operator can see the policy in force. It is
	// deliberately not what gets applied; see below.
	if _, err := e.writeSandboxProfile(body); err != nil {
		return nil, err
	}

	// The profile is passed inline rather than as a file path.
	//
	// With per-server accounts the process drops to the server's own uid
	// before sandbox-exec runs, so sandbox-exec would have to read the profile
	// as that account. The file lives in the server's runtime directory, which
	// is owned by whoever wings runs as and is not readable by the server, so
	// -f fails with a bare exit code 65 and no message. Passing the policy in
	// argv removes the dependency on file permissions entirely.
	//
	// The profile contains paths, not secrets, so it being visible in ps is
	// not a leak.
	//
	// sandbox-exec applies the policy and then execs, so the shell and every
	// process it goes on to spawn inherit the confinement.
	return append([]string{sandbox.ExecPath, "-p", body}, plain...), nil
}

// writeSandboxProfile stores the profile next to the server's other runtime
// plumbing and returns its path.
//
// It lives outside the server's data directory on purpose. Anywhere inside it
// would be both readable and writable by the very process being confined, and
// while rewriting the file after the fact would not lift a policy already
// applied, it would take effect on the next boot.
func (e *Environment) writeSandboxProfile(body string) (string, error) {
	dir := e.runtimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", errors.Wrap(err, "environment/native: failed to create the runtime directory")
	}
	path := filepath.Join(dir, sandboxProfileName)

	// Written atomically so a boot racing a rewrite cannot read a half-written
	// policy, which sandbox-exec would reject and turn into a failed start.
	tmp, err := os.CreateTemp(dir, ".sandbox.*.sb")
	if err != nil {
		return "", errors.Wrap(err, "environment/native: failed to stage the sandbox profile")
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return "", errors.Wrap(err, "environment/native: failed to write the sandbox profile")
	}
	if err := tmp.Close(); err != nil {
		return "", errors.Wrap(err, "environment/native: failed to write the sandbox profile")
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", errors.Wrap(err, "environment/native: failed to set permissions on the sandbox profile")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", errors.Wrap(err, "environment/native: failed to install the sandbox profile")
	}
	return path, nil
}
