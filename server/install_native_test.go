package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/remote"
	"github.com/pterodactyl/wings/server/filesystem"
)

// newNativeInstallTestProcess builds an installation process laid out the way
// `wings configure` lays a node out on macOS: the data and temporary
// directories both sit under the wings root, next to config.yml.
func newNativeInstallTestProcess(t *testing.T, script string) (*InstallationProcess, string) {
	t.Helper()

	// Resolved, because on macOS the test's temporary directory is reached
	// through a symlink and the script is handed the path as wings spells it.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	cfg, err := config.NewAtPath(filepath.Join(root, "config.yml"))
	require.NoError(t, err)
	cfg.AuthenticationToken = "test-token"
	cfg.System.RootDirectory = root
	cfg.System.Data = filepath.Join(root, "volumes")
	cfg.System.TmpDirectory = filepath.Join(root, "tmp")
	cfg.System.LogDirectory = filepath.Join(root, "logs")
	config.Set(cfg)

	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yml"), []byte("token: hunter2\n"), 0o600))

	s, err := New(nil)
	require.NoError(t, err)
	s.cfg.Uuid = installTestUUID
	s.fs, err = filesystem.New(filepath.Join(cfg.System.Data, installTestUUID), 0, nil, filesystem.Owner{})
	require.NoError(t, err)

	ip := &InstallationProcess{
		Server: s,
		Script: &remote.InstallationScript{Entrypoint: "sh", Script: script},
	}
	require.NoError(t, ip.writeScriptToDisk())
	return ip, root
}

func TestNativeInstallRunsTheScriptAgainstTheServerDirectory(t *testing.T) {
	ip, _ := newNativeInstallTestProcess(t, "cd /mnt/server && echo installed > marker\n")

	require.NoError(t, ip.executeNative())

	body, err := os.ReadFile(filepath.Join(ip.Server.Filesystem().Path(), "marker"))
	require.NoError(t, err)
	assert.Equal(t, "installed\n", string(body))
}

func TestNativeInstallCommandIsUnwrappedWithoutTheSandbox(t *testing.T) {
	ip, _ := newNativeInstallTestProcess(t, "true\n")

	argv, err := ip.installCommand("/bin/sh", "/tmp/install.sh")
	require.NoError(t, err)
	assert.Equal(t, []string{"/bin/sh", "/tmp/install.sh"}, argv)
}
