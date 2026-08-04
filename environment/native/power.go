package native

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"emperror.dev/errors"
	"github.com/apex/log"
	"golang.org/x/sys/unix"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/remote"
)

// adoptPollInterval is how often an adopted process is checked for liveness.
//
// A server that wings itself started is reaped through wait(2) and needs no
// polling. One inherited from a previous wings process is not our child, so
// there is nothing to wait on and its exit has to be noticed by other means.
const adoptPollInterval = time.Second

// OnBeforeStart prepares the runtime plumbing for a server.
//
// The Docker environment destroys and rebuilds the container here so that
// configuration synced from the Panel takes effect. There is no comparable
// long-lived object in this environment -- every setting is applied afresh
// when the process is spawned -- so this only has to make sure the runtime
// directory, FIFO and data directory exist.
func (e *Environment) OnBeforeStart(ctx context.Context) error {
	return e.Create()
}

// startupVariable matches the {{VARIABLE}} placeholders eggs use in their
// startup commands.
var startupVariable = regexp.MustCompile(`\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// startupCommand returns the command line the Panel wants run for this server.
//
// The Panel does *not* substitute the {{VARIABLE}} placeholders in a startup
// command; it sends them through verbatim in STARTUP and relies on the egg
// image's entrypoint to expand them, which yolks does by rewriting {{X}} to
// ${X} and letting a shell evaluate the result. There is no image here, so
// nothing would expand them and the server would be handed a jar named
// literally "{{SERVER_JARFILE}}".
//
// Expanding them here rather than deferring to the shell keeps the startup
// command from being re-evaluated: a variable whose value contains a quote or
// a $ would otherwise change the shape of the command being run.
func (e *Environment) startupCommand() (string, error) {
	env := e.Configuration.EnvironmentVariables()
	for _, v := range env {
		if name, value, ok := strings.Cut(v, "="); ok && name == "STARTUP" {
			if strings.TrimSpace(value) == "" {
				break
			}
			return expandStartupVariables(value, env), nil
		}
	}
	return "", errors.New("environment/native: server has no STARTUP command configured")
}

// expandStartupVariables replaces {{VARIABLE}} placeholders with values from
// the server's environment.
//
// An unknown placeholder becomes an empty string, which is what the shell
// expansion in the egg entrypoint would also produce, so a startup command
// referencing a variable the Panel did not send fails the same way on both
// environments rather than differently.
func expandStartupVariables(startup string, env []string) string {
	values := make(map[string]string, len(env))
	for _, v := range env {
		if name, value, ok := strings.Cut(v, "="); ok {
			values[name] = value
		}
	}
	return startupVariable.ReplaceAllStringFunc(startup, func(match string) string {
		name := startupVariable.FindStringSubmatch(match)[1]
		return values[name]
	})
}

// Start boots the server process and begins streaming its console.
func (e *Environment) Start(ctx context.Context) error {
	sawError := false

	// If sawError is set to true there was an error somewhere in the pipeline that
	// got passed up, but we also want to ensure we set the server to be offline at
	// that point.
	defer func() {
		if sawError {
			// If we don't set it to stopping first, you'll trigger crash detection which
			// we don't want to do at this point since it'll just immediately try to do the
			// exact same action that lead to it crashing in the first place...
			e.SetState(environment.ProcessStoppingState)
			e.SetState(environment.ProcessOfflineState)
		}
	}()

	// If a process is already running -- either ours, or one left behind by a
	// previous wings -- adopt it rather than starting a second copy.
	if pid := e.currentPid(); processIsAlive(pid) {
		e.adopt(pid)
		e.SetState(environment.ProcessRunningState)
		return e.Attach(ctx)
	}

	e.SetState(environment.ProcessStartingState)
	sawError = true

	// A fresh process has not been OOM killed; clear the flag from any previous
	// run so the Panel does not attribute an old kill to this one.
	e.mu.Lock()
	e.oomKilled = false
	e.mu.Unlock()

	if err := e.OnBeforeStart(ctx); err != nil {
		return errors.WrapIf(err, "environment/native: failed to run pre-boot process")
	}

	startup, err := e.startupCommand()
	if err != nil {
		return err
	}
	dir, err := e.workingDir()
	if err != nil {
		return err
	}

	// Truncate the console log so the websocket does not replay the previous
	// boot's output, mirroring what the Docker environment does to the
	// container log on start.
	logFile, err := os.OpenFile(e.logPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.Wrap(err, "environment/native: failed to open console log")
	}
	defer logFile.Close()

	// Attach before spawning so the FIFO has a writer and the console tail is
	// already following the (now empty) log.
	if err := e.Attach(ctx); err != nil {
		return errors.WrapIf(err, "environment/native: failed to attach to process")
	}

	e.mu.RLock()
	stdin := e.stdin
	e.mu.RUnlock()
	if stdin == nil {
		return errors.New("environment/native: stdin was not available after attaching")
	}

	cmd := exec.Command("/bin/sh", "-c", startup)
	cmd.Dir = dir
	cmd.Env = e.processEnvironment(dir)
	cmd.Stdin = stdin
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Give the server its own session, and therefore its own process
		// group. This does two things: it detaches the server from wings so it
		// survives wings restarting, and it means signals can be delivered to
		// the whole group so a shell wrapper cannot leave the real server
		// process orphaned.
		Setsid: true,
	}

	// Drop to this server's own account when one is configured, so that unix
	// permissions keep it away from other servers' files and from wings'
	// config.yml, which holds the node token.
	//
	// stdin, stdout and stderr are inherited file descriptors, so the server
	// needs no permission on the FIFO or the log file itself; only its data
	// directory has to be readable, and the filesystem layer owns that.
	e.mu.RLock()
	account := e.meta.Account
	e.mu.RUnlock()
	if account != nil {
		if os.Geteuid() != 0 {
			return errors.New("environment/native: per-server accounts require wings to run as root")
		}
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: uint32(account.UID),
			Gid: uint32(account.GID),
		}
		e.log().WithField("account", account.String()).Debug("running server under its own account")
	}

	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "environment/native: failed to start process")
	}

	pid := cmd.Process.Pid
	wait := make(chan struct{})

	e.mu.Lock()
	e.pid = pid
	e.wait = wait
	e.mu.Unlock()

	if err := e.writePidFile(pid); err != nil {
		e.log().WithField("error", err).Warn("failed to record process id")
	}
	e.log().WithField("pid", pid).WithField("image", e.meta.Image).Info("started server process")

	// Reap the process and report its exit.
	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
				if code < 0 {
					// Killed by a signal; report the shell convention of
					// 128+signal so the Panel shows something meaningful.
					if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
						code = 128 + int(ws.Signal())
					}
				}
			} else {
				e.log().WithField("error", err).Warn("error while waiting on server process")
			}
		}
		e.onProcessExit(pid, uint32(code), wait)
	}()

	sawError = false
	return nil
}

// adopt takes ownership of a server process that wings did not start, which
// happens when wings is restarted while a server keeps running.
func (e *Environment) adopt(pid int) {
	e.mu.Lock()
	if e.wait != nil {
		// Already tracking a process.
		e.mu.Unlock()
		return
	}
	wait := make(chan struct{})
	e.pid = pid
	e.wait = wait
	e.mu.Unlock()

	e.log().WithField("pid", pid).Info("adopted running server process from a previous wings instance")

	// The process is not our child, so wait(2) is unavailable and its exit has
	// to be detected by polling.
	go func() {
		for {
			if !processIsAlive(pid) {
				// The real exit status is unavailable for a process we did not
				// spawn. Report a non-zero code so that an unexpected exit is
				// still treated as a crash.
				e.onProcessExit(pid, 1, wait)
				return
			}
			time.Sleep(adoptPollInterval)
		}
	}()
}

// onProcessExit records the exit of a server process and moves the environment
// into its offline state.
func (e *Environment) onProcessExit(pid int, code uint32, wait chan struct{}) {
	e.mu.Lock()
	// Guard against a stale goroutine from an earlier process clobbering the
	// state of a newer one.
	if e.pid != pid {
		e.mu.Unlock()
		return
	}
	e.pid = 0
	e.exitCode = code
	e.wait = nil
	e.mu.Unlock()

	// Release anything blocked in WaitForStop. The pid guard above means this
	// runs exactly once per process, so the close cannot happen twice.
	close(wait)

	_ = os.Remove(e.pidPath())
	e.log().WithField("pid", pid).WithField("exit_code", code).Info("server process exited")

	// Tear down the console tail and resource polling, then report offline.
	// Ordering matters: the state change is what triggers crash detection, and
	// it should see a fully stopped environment.
	e.detach()
	e.SetState(environment.ProcessOfflineState)
}

// processEnvironment builds the environment for the server process.
func (e *Environment) processEnvironment(dir string) []string {
	cfg := config.Get()

	// Start from what the Panel sent. Unlike the Docker environment there is no
	// bridge network to redirect SERVER_IP onto, so the value is left alone.
	vars := append([]string(nil), e.Configuration.EnvironmentVariables()...)

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

	// The egg's image would normally supply these. Without a container the
	// process inherits nothing, so provide sensible values.
	add("HOME", dir)
	add("PWD", dir)
	add("USER", cfg.System.Username)
	add("TZ", cfg.System.Timezone)
	add("PATH", os.Getenv("PATH"))
	add("LANG", "en_US.UTF-8")

	return vars
}

// Stop requests that the server shut down, using whatever mechanism the Panel
// configured for its egg.
//
// You most likely want to be using WaitForStop() rather than this function,
// since this will return as soon as the command is sent, rather than waiting
// for the process to be completed stopped.
func (e *Environment) Stop(ctx context.Context) error {
	e.mu.RLock()
	s := e.meta.Stop
	e.mu.RUnlock()

	// If the process is already offline don't switch it back to stopping. Just leave it how
	// it is and continue through to the stop handling for the process.
	if e.st.Load() != environment.ProcessOfflineState {
		e.SetState(environment.ProcessStoppingState)
	}

	if !processIsAlive(e.currentPid()) {
		e.SetState(environment.ProcessOfflineState)
		return nil
	}

	// Handle signal based actions.
	if s.Type == remote.ProcessStopSignal {
		log.WithField("signal_value", s.Value).Debug("stopping server using signal")
		return e.signalProcess(signalFromName(s.Value))
	}

	// Handle command based stops. Only attempt to send the stop command to the
	// instance if we are actually attached to it.
	if e.IsAttached() && s.Type == remote.ProcessStopCommand {
		return e.SendCommand(s.Value)
	}

	if s.Type == "" {
		e.log().Warn("no stop configuration detected for environment, using SIGTERM")
	}

	return e.signalProcess(unix.SIGTERM)
}

// WaitForStop attempts to gracefully stop a server using the defined stop
// command. If the server does not stop after seconds have passed, an error will
// be returned, or the instance will be terminated forcefully depending on the
// value of the second argument.
func (e *Environment) WaitForStop(ctx context.Context, duration time.Duration, terminate bool) error {
	tctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	// If the parent context is canceled, abort the timed context for termination.
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-tctx.Done():
		}
	}()

	doTermination := func(s string) error {
		e.log().WithField("step", s).WithField("duration", duration).Warn("process stop did not complete in time, terminating process...")
		return e.Terminate(ctx, "SIGKILL")
	}

	if err := e.Stop(tctx); err != nil {
		if terminate && errors.Is(err, context.DeadlineExceeded) {
			return doTermination("stop")
		}
		return err
	}

	// Block until the process is reaped, the deadline passes, or the caller
	// gives up.
	e.mu.RLock()
	wait := e.wait
	e.mu.RUnlock()

	if wait == nil {
		// Nothing is being tracked, so the process is already gone.
		return nil
	}

	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		if terminate {
			return doTermination("parent-context")
		}
		return ctx.Err()
	case <-tctx.Done():
		if terminate {
			return doTermination("wait")
		}
		return errors.WrapIf(tctx.Err(), "environment/native: error waiting on process to enter \"not-running\" state")
	}
}

// Terminate forcefully stops the server with the given signal and marks the
// environment offline.
func (e *Environment) Terminate(ctx context.Context, signal string) error {
	pid := e.currentPid()
	if !processIsAlive(pid) {
		// If the process is not running, but we're not already in a stopped state go
		// ahead and update things to indicate we should be completely stopped now.
		// Set to stopping first so crash detection is not triggered.
		if e.st.Load() != environment.ProcessOfflineState {
			e.SetState(environment.ProcessStoppingState)
			e.SetState(environment.ProcessOfflineState)
		}
		return nil
	}

	// We set it to stopping then offline to prevent crash detection from being triggered.
	e.SetState(environment.ProcessStoppingState)

	if err := e.signalProcess(signalFromName(signal)); err != nil {
		return err
	}

	e.SetState(environment.ProcessOfflineState)
	return nil
}

// signalProcess delivers a signal to the server's process group.
//
// Addressing the group rather than the pid matters because the startup command
// runs under a shell: signalling only the shell would leave the actual server
// running and unsupervised.
func (e *Environment) signalProcess(sig unix.Signal) error {
	pid := e.currentPid()
	if !processIsAlive(pid) {
		return nil
	}

	// Setsid made the process a group leader, so its pgid equals its pid; a
	// negative pid addresses the whole group.
	if err := unix.Kill(-pid, sig); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		// Fall back to signalling just the process in case it managed to leave
		// its own group.
		if err := unix.Kill(pid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
			return errors.Wrap(err, "environment/native: failed to signal process")
		}
	}
	return nil
}

// signalFromName maps the signal names the Panel may send onto real signals,
// defaulting to SIGKILL exactly as the Docker environment does.
func signalFromName(name string) unix.Signal {
	switch strings.ToUpper(strings.TrimPrefix(strings.ToUpper(name), "SIG")) {
	case "ABRT":
		return unix.SIGABRT
	case "INT", "C":
		return unix.SIGINT
	case "TERM":
		return unix.SIGTERM
	case "QUIT":
		return unix.SIGQUIT
	case "HUP":
		return unix.SIGHUP
	case "KILL":
		return unix.SIGKILL
	default:
		log.WithField("signal", name).Info("Unrecognised signal requested, defaulting to SIGKILL")
		return unix.SIGKILL
	}
}
