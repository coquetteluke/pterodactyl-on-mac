//go:build darwin

package native

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/internal/serveruser"
	"github.com/pterodactyl/wings/remote"
)

// RLIMIT_NPROC counts every process owned by a uid, not a process tree. With no
// dedicated account a server shares the uid wings runs as, so the limit would
// be spent by every server together plus wings itself, and the first one to
// start would set a budget for the whole machine.
func TestProcessLimitSkippedWithoutDedicatedAccount(t *testing.T) {
	e := newTestEnvironment(t, "run", remote.ProcessStopConfiguration{})
	cfg := config.Get()
	cfg.Docker.ContainerPidLimit = 64
	config.Set(cfg)

	if got := e.applyProcessLimit("run"); got != "run" {
		t.Errorf("expected the startup to be untouched without a dedicated uid, got %q", got)
	}
}

func TestProcessLimitAppliedWithDedicatedAccount(t *testing.T) {
	e := newTestEnvironment(t, "run", remote.ProcessStopConfiguration{})
	e.meta.Account = &serveruser.Account{Username: "ptero-test", UID: 900, GID: 900}

	cfg := config.Get()
	cfg.Docker.ContainerPidLimit = 64
	config.Set(cfg)

	got := e.applyProcessLimit("run")
	if !strings.Contains(got, "ulimit -u 64") {
		t.Errorf("expected the process limit to be applied, got %q", got)
	}
	// A limit that silently failed to apply is indistinguishable from one that
	// worked, which defeats the point of having it.
	if !strings.Contains(got, "exit 1") {
		t.Errorf("expected the boot to fail if the limit cannot be set, got %q", got)
	}
	// The server has to replace the shell, or a stop signal ends the shell
	// and the supervisor with it while the server is still shutting down.
	if !strings.Contains(got, "\nexec /bin/sh -c 'run'") {
		t.Errorf("expected the startup command to be exec'd, got %q", got)
	}
}

func TestShellQuoteRoundTrips(t *testing.T) {
	for _, s := range []string{"run", "java -jar 'a b'.jar", `echo "$HOME" \ it's`, ""} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shellQuote(%q) came back as %q (%v)", s, out, err)
		}
	}
}

func TestProcessLimitDisabledWhenZero(t *testing.T) {
	e := newTestEnvironment(t, "run", remote.ProcessStopConfiguration{})
	e.meta.Account = &serveruser.Account{Username: "ptero-test", UID: 900, GID: 900}

	cfg := config.Get()
	cfg.Docker.ContainerPidLimit = 0
	config.Set(cfg)

	if got := e.applyProcessLimit("run"); got != "run" {
		t.Errorf("expected no limit when configured to 0, got %q", got)
	}
}
