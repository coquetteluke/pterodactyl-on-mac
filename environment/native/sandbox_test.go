//go:build darwin

package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/remote"
)

func TestSandboxCommandDisabledIsUnwrapped(t *testing.T) {
	e := newTestEnvironment(t, "sleep 1", remote.ProcessStopConfiguration{})
	argv, err := e.sandboxCommand(t.TempDir(), "sleep 1")
	if err != nil {
		t.Fatalf("sandboxCommand: %v", err)
	}
	if argv[0] != "/bin/sh" {
		t.Errorf("expected an unwrapped command when the sandbox is off, got %v", argv)
	}
}

// The profile must be passed inline, not as a file path.
//
// With per-server accounts the process drops to the server's own uid before
// sandbox-exec runs. The profile file lives in the server's runtime directory,
// owned by whoever wings runs as and unreadable by that account, so -f fails
// with a bare exit code 65 and no diagnostic at all. This test exists because
// that is exactly what shipped once.
func TestSandboxCommandPassesProfileInline(t *testing.T) {
	root := t.TempDir()
	e := newTestEnvironmentAt(t, root, "sleep 1", remote.ProcessStopConfiguration{})

	cfg := config.Get()
	cfg.System.Sandbox.Enabled = true
	config.Set(cfg)

	serverDir := filepath.Join(root, "servers", "test")
	argv, err := e.sandboxCommand(serverDir, "sleep 1")
	if err != nil {
		t.Fatalf("sandboxCommand: %v", err)
	}

	if len(argv) < 3 {
		t.Fatalf("expected a wrapped command, got %v", argv)
	}
	if !strings.HasSuffix(argv[0], "sandbox-exec") {
		t.Errorf("expected sandbox-exec, got %q", argv[0])
	}
	if argv[1] == "-f" {
		t.Fatal("profile passed by path: a server dropped to its own account cannot read it, and the failure is a bare exit code 65")
	}
	if argv[1] != "-p" {
		t.Fatalf("expected -p, got %q", argv[1])
	}
	// argv[2] must be the policy itself rather than a path to it.
	if !strings.Contains(argv[2], "(version 1)") || !strings.Contains(argv[2], "(deny file-read*") {
		t.Errorf("expected the profile body inline, got %q", argv[2])
	}
	if !strings.Contains(argv[2], serverDir) {
		t.Errorf("expected the server's own directory to be allowed in the profile, got %q", argv[2])
	}
}

// The written copy is for an operator to read, so it should still appear even
// though it is not what gets applied.
func TestSandboxProfileIsWrittenForInspection(t *testing.T) {
	root := t.TempDir()
	e := newTestEnvironmentAt(t, root, "sleep 1", remote.ProcessStopConfiguration{})

	cfg := config.Get()
	cfg.System.Sandbox.Enabled = true
	config.Set(cfg)

	if _, err := e.sandboxCommand(filepath.Join(root, "servers", "test"), "sleep 1"); err != nil {
		t.Fatalf("sandboxCommand: %v", err)
	}
	path := filepath.Join(e.runtimeDir(), sandboxProfileName)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected the profile to be written for inspection: %v", err)
	}
}
