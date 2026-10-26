package server

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"

	"emperror.dev/errors"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/internal/serveruser"
	"github.com/pterodactyl/wings/system"
)

// Egg install scripts are written against the layout of the installer
// container, where the server's files live at /mnt/server and the script
// itself at /mnt/install. Without a container there is nothing to mount those
// paths onto -- and on macOS the root filesystem is sealed, so /mnt cannot
// even be created without disabling SIP -- so the paths are rewritten to their
// real locations before the script runs.
//
// This handles scripts that reference the paths literally, which is nearly all
// of them. A script that assembles a path at runtime (`cd /mnt/${dir}`) will
// not be caught and will fail; that is a known limitation of running installs
// without a container.
const (
	containerServerPath  = "/mnt/server"
	containerInstallPath = "/mnt/install"
)

// executeNative runs an egg's installation script directly on the host.
//
// The Docker implementation gets isolation, a guaranteed set of tooling from
// the installer image, and resource limits. None of that is available here:
// the script runs as the wings user against whatever binaries exist on the
// host's PATH. Scripts that assume a Debian or Alpine userland may need
// packages installed with Homebrew first.
func (ip *InstallationProcess) executeNative() error {
	ctx, cancel := context.WithCancel(ip.Server.Context())
	defer cancel()

	// Ensure the root directory for the server exists properly before attempting
	// to trigger the reinstall of the server.
	if err := ip.Server.EnsureDataDirectoryExists(); err != nil {
		return err
	}

	defer func() {
		if err := os.RemoveAll(ip.tempDir()); err != nil && !os.IsNotExist(err) {
			ip.Server.Log().WithField("error", err).Warn("failed to remove temporary data directory after install process")
		}
	}()

	scriptPath, err := ip.writeNativeScript()
	if err != nil {
		return err
	}

	account, err := ip.installAccount()
	if err != nil {
		return err
	}
	if account != nil {
		// The script runs as the server's account, so that account has to be
		// able to read it and to write the files it installs.
		base := filepath.Clean(config.Get().System.TmpDirectory) + string(os.PathSeparator)
		for _, p := range []string{ip.tempDir(), scriptPath} {
			// Both are derived from the server's id. Refuse anything that
			// would land outside the temporary directory rather than chown it.
			p = filepath.Clean(p)
			if !strings.HasPrefix(p, base) {
				return errors.New("install: refusing to chown a path outside the temporary directory: " + p)
			}
			if err := os.Chown(p, account.UID, account.GID); err != nil {
				return errors.Wrap(err, "install: failed to hand the installation script to the server's account")
			}
		}
		if err := ip.Server.Filesystem().Chown("/"); err != nil {
			return errors.WithMessage(err, "install: failed to hand the server's files to its account")
		}
	}

	interpreter := resolveInterpreter(ip.Script.Entrypoint)
	ip.Server.Log().
		WithField("install_script", scriptPath).
		WithField("interpreter", interpreter).
		Info("running installation script as a host process")

	logFile, err := ip.openInstallLog()
	if err != nil {
		return err
	}
	defer logFile.Close()

	// Output goes to two places, exactly as it does under Docker: the install
	// log on disk, and the websocket sink the Panel reads.
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := system.ScanReader(pr, ip.Server.Sink(system.InstallSink).Push); err != nil && !errors.Is(err, context.Canceled) {
			ip.Server.Log().WithField("error", err).Warn("error processing install output lines")
		}
	}()

	cmd := exec.CommandContext(ctx, interpreter, scriptPath)
	cmd.Dir = ip.Server.Filesystem().Path()
	cmd.Env = ip.nativeEnvironment()
	cmd.Stdout = io.MultiWriter(logFile, pw)
	cmd.Stderr = io.MultiWriter(logFile, pw)
	// Its own process group, so that cancelling the install, and finishing it,
	// can take anything the script left running in the background with it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	if account != nil {
		// With per-server accounts wings runs as root, and an install script
		// is egg code fed with user-set variables. Running it as root would
		// hand it the whole machine, so it gets the server's account, exactly
		// as the server process does.
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(account.UID),
			Gid: uint32(account.GID),
		}
	}

	runErr := cmd.Run()
	if cmd.Process != nil {
		// The group cannot be reused while any member is alive, and once none
		// is this fails with ESRCH, so it cannot reach anything else.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	// Stop the scanner and wait for it to drain before returning, so the last
	// lines of output are not lost.
	_ = pw.Close()
	<-done
	_ = pr.Close()

	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			return errors.Wrapf(runErr, "install: script exited with code %d", ee.ExitCode())
		}
		return errors.Wrap(runErr, "install: failed to run installation script")
	}
	return nil
}

// installAccount returns the account the install script should run as, or nil
// to run it as wings itself. It matches the account the server process runs as.
func (ip *InstallationProcess) installAccount() (*serveruser.Account, error) {
	if !config.Get().System.User.PerServer || os.Geteuid() != 0 {
		return nil, nil
	}
	acct, ok, err := serveruser.Lookup(ip.Server.ID())
	if err != nil {
		return nil, errors.WrapIf(err, "install: could not look up the server's account")
	}
	if !ok {
		// InitServer provisions the account before a server can be installed,
		// so this means it was deleted underneath wings. Refuse rather than
		// fall back to running the script as root.
		return nil, errors.New("install: the server's dedicated account does not exist; restart wings to recreate it")
	}
	return &acct, nil
}

// writeNativeScript rewrites the container paths in the install script to the
// real directories on this host and writes it out, returning its path.
func (ip *InstallationProcess) writeNativeScript() (string, error) {
	script := strings.ReplaceAll(ip.Script.Script, "\r\n", "\n")
	script = strings.ReplaceAll(script, containerServerPath, ip.Server.Filesystem().Path())
	script = strings.ReplaceAll(script, containerInstallPath, ip.tempDir())

	path := filepath.Join(ip.tempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", errors.WithMessage(err, "install: failed to write rewritten installation script")
	}
	return path, nil
}

// nativeEnvironment builds the environment for the install script.
func (ip *InstallationProcess) nativeEnvironment() []string {
	vars := append([]string(nil), ip.Server.GetEnvironmentVariables()...)

	seen := make(map[string]struct{}, len(vars))
	for _, v := range vars {
		if name, _, ok := strings.Cut(v, "="); ok {
			seen[name] = struct{}{}
		}
	}
	add := func(k, v string) {
		if _, ok := seen[k]; !ok && v != "" {
			vars = append(vars, k+"="+v)
		}
	}

	// The installer image would normally provide these.
	add("HOME", ip.Server.Filesystem().Path())
	add("PATH", os.Getenv("PATH"))
	add("TZ", config.Get().System.Timezone)
	add("LANG", "en_US.UTF-8")

	return vars
}

// openInstallLog truncates and opens the install log, writing the same header
// the Docker path writes so the two produce comparable output.
func (ip *InstallationProcess) openInstallLog() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(ip.GetLogPath()), 0o700); err != nil {
		return nil, errors.WithMessage(err, "install: could not create log directory")
	}
	f, err := os.OpenFile(ip.GetLogPath(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}

	tmpl, err := template.New("header").Parse(`Pterodactyl Server Installation Log

|
| Details
| ------------------------------
  Server UUID:          {{.Server.ID}}
  Environment:          native (no container)
  Container Image:      {{.Script.ContainerImage}} (not used)
  Script Interpreter:   {{.Script.Entrypoint}}

|
| Environment Variables
| ------------------------------
{{ range $key, $value := .Server.GetEnvironmentVariables }}  {{ $value }}
{{ end }}

|
| Script Output
| ------------------------------
`)
	if err != nil {
		f.Close()
		return nil, err
	}
	if err := tmpl.Execute(f, ip); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// resolveInterpreter maps the egg's entrypoint onto a shell that exists on this
// host.
//
// Eggs commonly specify "ash", which is BusyBox's shell and only exists in the
// Alpine-based installer images. Every such script is POSIX sh compatible, so
// /bin/sh is the correct substitute.
func resolveInterpreter(entrypoint string) string {
	name := strings.TrimSpace(entrypoint)
	if name == "" {
		return "/bin/sh"
	}
	// An absolute path that exists is used as-is.
	if strings.HasPrefix(name, "/") {
		if _, err := os.Stat(name); err == nil {
			return name
		}
		name = filepath.Base(name)
	}
	if name == "ash" || name == "dash" {
		return "/bin/sh"
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return "/bin/sh"
}
