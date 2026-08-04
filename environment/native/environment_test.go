//go:build darwin

package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/internal/serveruser"
	"github.com/pterodactyl/wings/remote"

	"golang.org/x/sys/unix"
)

// newTestEnvironment builds a native environment whose "server" is a shell loop
// that echoes whatever is sent to its stdin and exits when told to stop. That
// is enough to exercise the whole console path: FIFO stdin, log tailing,
// process group signalling and exit reporting.
func newTestEnvironment(t *testing.T, startup string, stop remote.ProcessStopConfiguration) *Environment {
	t.Helper()
	return newTestEnvironmentAt(t, t.TempDir(), startup, stop)
}

func newTestEnvironmentAt(t *testing.T, root, startup string, stop remote.ProcessStopConfiguration) *Environment {
	t.Helper()

	serverDir := filepath.Join(root, "servers", "test")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatalf("mkdir server dir: %v", err)
	}

	config.Set(&config.Configuration{
		// config.Set derives a JWT key from this and panics if it is empty.
		AuthenticationToken: "test-token",
		System: config.SystemConfiguration{
			RootDirectory: root,
			Environment:   "native",
			Timezone:      "UTC",
			Username:      "pterodactyl",
		},
	})

	envCfg := environment.NewConfiguration(environment.Settings{
		Mounts: []environment.Mount{{Default: true, Source: serverDir, Target: "/home/container"}},
		Limits: environment.Limits{MemoryLimit: 128},
	}, []string{"STARTUP=" + startup})

	e, err := New("test-server", &Metadata{Stop: stop}, envCfg)
	if err != nil {
		t.Fatalf("new environment: %v", err)
	}
	t.Cleanup(func() {
		_ = e.Terminate(context.Background(), "SIGKILL")
	})
	return e
}

// echoLoop reads stdin forever, echoing each line, and exits on "stop".
const echoLoop = `while IFS= read -r line; do echo "got:$line"; if [ "$line" = "stop" ]; then echo "bye"; exit 7; fi; done`

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestNativeEnvironment_ConsoleRoundTrip(t *testing.T) {
	e := newTestEnvironment(t, echoLoop, remote.ProcessStopConfiguration{
		Type:  remote.ProcessStopCommand,
		Value: "stop",
	})

	var mu sync.Mutex
	var lines []string
	e.SetLogCallback(func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, string(b))
	})
	hasLine := func(want string) func() bool {
		return func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, l := range lines {
				if strings.TrimSpace(l) == want {
					return true
				}
			}
			return false
		}
	}

	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFor(t, "process to be running", func() bool {
		ok, _ := e.IsRunning(context.Background())
		return ok
	})

	// Commands must reach the process through the FIFO, and its output must
	// come back out of the tailed log file.
	if err := e.SendCommand("hello"); err != nil {
		t.Fatalf("send command: %v", err)
	}
	waitFor(t, "echoed command", hasLine("got:hello"))

	// Uptime is derived from the kernel's process start time.
	up, err := e.Uptime(context.Background())
	if err != nil {
		t.Fatalf("uptime: %v", err)
	}
	if up <= 0 {
		t.Fatalf("expected positive uptime, got %d", up)
	}

	// Readlog reads the same output back off disk.
	logged, err := e.Readlog(50)
	if err != nil {
		t.Fatalf("readlog: %v", err)
	}
	if !containsLine(logged, "got:hello") {
		t.Fatalf("expected readlog to contain the echoed command, got %q", logged)
	}

	// A stop command exits the process, and the exit code is reported.
	// WaitForStop must return as soon as the process is reaped rather than
	// sitting on its timeout, so time the call and require it to be prompt.
	stopStart := time.Now()
	if err := e.WaitForStop(context.Background(), 10*time.Second, true); err != nil {
		t.Fatalf("wait for stop: %v", err)
	}
	if elapsed := time.Since(stopStart); elapsed > 5*time.Second {
		t.Fatalf("WaitForStop blocked for %v; it should return when the process exits, not when the timeout expires", elapsed)
	}
	waitFor(t, "offline state", func() bool {
		return e.State() == environment.ProcessOfflineState
	})

	code, oom, err := e.ExitState()
	if err != nil {
		t.Fatalf("exit state: %v", err)
	}
	if code != 7 {
		t.Fatalf("expected exit code 7 from the stop command, got %d", code)
	}
	if oom {
		t.Fatal("darwin has no OOM killer; oom should never be reported")
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}

func TestNativeEnvironment_TerminateKillsProcessGroup(t *testing.T) {
	// A startup command that is not a simple exec: the shell stays alive as
	// the group leader with a child doing the real work. Signalling only the
	// leader would leave the child running, which is what the process group
	// handling exists to prevent.
	e := newTestEnvironment(t, `sleep 300 & echo "child:$!"; wait`, remote.ProcessStopConfiguration{
		Type:  remote.ProcessStopSignal,
		Value: "SIGTERM",
	})

	var mu sync.Mutex
	var out []string
	e.SetLogCallback(func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		out = append(out, string(b))
	})

	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "child pid announcement", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(out) > 0
	})

	mu.Lock()
	line := strings.TrimSpace(out[0])
	mu.Unlock()

	var childPid int
	if _, err := fmtSscan(line, &childPid); err != nil {
		t.Fatalf("could not parse child pid from %q: %v", line, err)
	}
	if !processIsAlive(childPid) {
		t.Fatalf("child %d should be alive", childPid)
	}

	if err := e.Terminate(context.Background(), "SIGKILL"); err != nil {
		t.Fatalf("terminate: %v", err)
	}

	waitFor(t, "child process to die with the group", func() bool {
		return !processIsAlive(childPid)
	})
}

// fmtSscan parses "child:<pid>".
func fmtSscan(line string, pid *int) (int, error) {
	_, after, _ := strings.Cut(line, ":")
	var n int
	var err error
	n, err = parseInt(strings.TrimSpace(after))
	*pid = n
	return n, err
}

func parseInt(s string) (int, error) {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(r-'0')
	}
	if s == "" {
		return 0, os.ErrInvalid
	}
	return n, nil
}

// A server that wings re-attaches to on boot must still be watched, otherwise
// its death goes unnoticed and the Panel shows a dead server as running
// forever. wings does not call Start() on that path, so the watch has to be
// established by Attach() too.
func TestNativeEnvironment_AdoptedServerExitIsNoticed(t *testing.T) {
	e := newTestEnvironment(t, `while :; do sleep 1; done`, remote.ProcessStopConfiguration{
		Type:  remote.ProcessStopSignal,
		Value: "SIGKILL",
	})

	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "process to be running", func() bool {
		ok, _ := e.IsRunning(context.Background())
		return ok
	})
	pid := e.currentPid()
	if pid <= 0 {
		t.Fatal("expected a live pid")
	}

	// Stand up a second environment over the same on-disk state and attach to
	// it, which is exactly what a restarted wings does. Nothing calls Start().
	e2, err := New(e.Id, &Metadata{}, e.Configuration)
	if err != nil {
		t.Fatalf("second environment: %v", err)
	}
	if err := e2.Attach(context.Background()); err != nil {
		t.Fatalf("attach: %v", err)
	}
	e2.SetState(environment.ProcessRunningState)

	if err := unix.Kill(pid, unix.SIGKILL); err != nil {
		t.Fatalf("kill: %v", err)
	}

	waitFor(t, "the adopted process's death to be noticed", func() bool {
		return e2.State() == environment.ProcessOfflineState
	})

	if running, _ := e2.IsRunning(context.Background()); running {
		t.Fatal("IsRunning still reports true after the process died")
	}
}

// A recycled pid must not be mistaken for the server. The pid file records the
// process start time precisely so that a stale entry cannot resurrect a dead
// server.
func TestNativeEnvironment_StalePidIsNotTrusted(t *testing.T) {
	e := newTestEnvironment(t, `while :; do sleep 1; done`, remote.ProcessStopConfiguration{})
	if err := e.Create(); err != nil {
		t.Fatalf("create: %v", err)
	}

	// pid 1 is alive and always will be, but it is not our server, and its
	// start time will not match the bogus value recorded here.
	if err := os.WriteFile(e.pidPath(), []byte("1 123456789\n"), 0o600); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
	if got := e.readPidFile(); got != 0 {
		t.Fatalf("expected a mismatched start time to invalidate the pid, got %d", got)
	}
	if running, _ := e.IsRunning(context.Background()); running {
		t.Fatal("a recycled pid should not be reported as the server running")
	}
}

// The whole point of a per-server account is that the server process actually
// runs as it, and therefore cannot read files belonging to another server.
// Verifying that needs root, so this is skipped for an ordinary `go test` and
// is the reason the suite is also run under sudo in CI.
func TestNativeEnvironment_RunsUnderDedicatedAccount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing a process's user requires root")
	}

	// "nobody" stands in for a provisioned per-server account so the test does
	// not have to create and tear down a real one.
	const nobodyUID = 4294967294 // (uid_t)-2

	// t.TempDir() is 0700 and root-owned, which the dropped-privilege process
	// cannot even traverse into, so use a path it can reach.
	root, err := os.MkdirTemp("/private/tmp", "ptero-iso")
	if err != nil {
		t.Fatalf("temp root: %v", err)
	}
	defer os.RemoveAll(root)

	e := newTestEnvironmentAt(t, root, `id -u > uid.txt; cat /private/tmp/ptero-secret-probe > stolen.txt 2>/dev/null; cat ../neighbour/world.dat > peeked.txt 2>/dev/null; while :; do sleep 1; done`,
		remote.ProcessStopConfiguration{Type: remote.ProcessStopSignal, Value: "SIGKILL"})
	e.meta.Account = &serveruser.Account{Username: "nobody", UID: nobodyUID, GID: nobodyUID}

	dir, err := e.workingDir()
	if err != nil {
		t.Fatalf("working dir: %v", err)
	}
	for _, p := range []string{root, filepath.Dir(dir)} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", p, err)
		}
	}
	// The account has to be able to write in its own data directory, which is
	// what the filesystem layer's chown does in production.
	if err := os.Chown(dir, nobodyUID, nobodyUID); err != nil {
		t.Fatalf("chown server dir: %v", err)
	}

	// A root-owned secret standing in for wings' own config.yml, which holds
	// the node token.
	secret := "/private/tmp/ptero-secret-probe"
	if err := os.WriteFile(secret, []byte("node-token"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	defer os.Remove(secret)
	if err := os.Chown(secret, 0, 0); err != nil {
		t.Fatalf("chown secret: %v", err)
	}

	// A second server, owned by a different account, with a world-readable
	// file inside it. This is the case chowning alone does not cover: the file
	// is mode 0644, so only the directory's own permissions stop our server
	// from walking in and reading it.
	const otherUID = 1 // daemon
	neighbour := filepath.Join(root, "servers", "neighbour")
	if err := os.MkdirAll(neighbour, 0o750); err != nil {
		t.Fatalf("mkdir neighbour: %v", err)
	}
	neighbourFile := filepath.Join(neighbour, "world.dat")
	if err := os.WriteFile(neighbourFile, []byte("neighbour-data"), 0o644); err != nil {
		t.Fatalf("write neighbour file: %v", err)
	}
	for _, p := range []string{neighbour, neighbourFile} {
		if err := os.Chown(p, otherUID, otherUID); err != nil {
			t.Fatalf("chown %s: %v", p, err)
		}
	}
	if err := os.Chmod(neighbour, 0o750); err != nil {
		t.Fatalf("chmod neighbour: %v", err)
	}

	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	uidFile := filepath.Join(dir, "uid.txt")
	waitFor(t, "the server to report its uid", func() bool {
		b, err := os.ReadFile(uidFile)
		return err == nil && strings.TrimSpace(string(b)) != ""
	})

	b, _ := os.ReadFile(uidFile)
	if got := strings.TrimSpace(string(b)); got != "4294967294" {
		t.Fatalf("server ran as uid %q, expected it to drop to nobody", got)
	}

	// The root-owned secret must not have been readable.
	if b, err := os.ReadFile(filepath.Join(dir, "stolen.txt")); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		t.Fatalf("server read a root-owned file it should not have access to: %q", b)
	}

	// Nor another server's data, even though that file is mode 0644. This is
	// the case that chowning alone would miss.
	if b, err := os.ReadFile(filepath.Join(dir, "peeked.txt")); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		t.Fatalf("server read a neighbouring server's file: %q", b)
	}
}

func TestNativeEnvironment_StatsReflectRealUsage(t *testing.T) {
	// Burn CPU so the sampler has something unambiguous to measure.
	e := newTestEnvironment(t, `a=0; while :; do a=$((a+1)); done`, remote.ProcessStopConfiguration{
		Type:  remote.ProcessStopSignal,
		Value: "SIGKILL",
	})

	// The bus emits JSON-encoded events, so decode them back into Stats.
	raw := make(chan []byte, 32)
	e.Events().On(raw)

	stats := make(chan environment.Stats, 8)
	go func() {
		for b := range raw {
			var ev struct {
				Topic string
				Data  environment.Stats
			}
			if err := json.Unmarshal(b, &ev); err != nil || ev.Topic != environment.ResourceEvent {
				continue
			}
			select {
			case stats <- ev.Data:
			default:
			}
		}
	}()

	if err := e.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The first sample has no previous reading to difference against, so wait
	// for one that carries a real CPU figure.
	deadline := time.After(15 * time.Second)
	for {
		select {
		case s := <-stats:
			if s.CpuAbsolute <= 0 {
				continue
			}
			if s.Memory == 0 {
				t.Fatalf("expected non-zero resident memory, got %+v", s)
			}
			if s.MemoryLimit == 0 {
				t.Fatalf("expected the configured memory limit to be reported, got %+v", s)
			}
			if s.Uptime <= 0 {
				t.Fatalf("expected positive uptime, got %+v", s)
			}
			// A single busy shell loop should be pegging roughly one core.
			if s.CpuAbsolute < 20 {
				t.Fatalf("expected a busy loop to show meaningful cpu, got %.2f", s.CpuAbsolute)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for a resource sample with cpu usage")
		}
	}
}
