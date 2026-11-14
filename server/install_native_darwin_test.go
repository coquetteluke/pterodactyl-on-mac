//go:build darwin

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/internal/sandbox"
)

func enableSandboxForTest(t *testing.T) {
	t.Helper()
	if !sandbox.Supported() {
		t.Skip("sandbox-exec not available")
	}
	cfg := config.Get()
	cfg.System.Sandbox.Enabled = true
	config.Set(cfg)
}

func TestNativeInstallCommandIsWrappedInTheSandbox(t *testing.T) {
	ip, root := newNativeInstallTestProcess(t, "true\n")
	enableSandboxForTest(t)

	script := filepath.Join(ip.tempDir(), "install.sh")
	argv, err := ip.installCommand("/bin/sh", script)
	require.NoError(t, err)
	require.Len(t, argv, 5)

	assert.Equal(t, sandbox.ExecPath, argv[0])
	// Inline, as for the server process: with per-server accounts the script
	// has already dropped to an account that could not read a profile file.
	assert.Equal(t, "-p", argv[1])
	assert.Contains(t, argv[2], `(deny file-read* file-write* (subpath "`+root+`"))`)
	assert.Contains(t, argv[2], `(allow file-read* file-write* (subpath "`+ip.tempDir()+`"))`)
	assert.Contains(t, argv[2], `(allow file-read* file-write* (subpath "`+ip.Server.Filesystem().Path()+`"))`)
	assert.Equal(t, []string{"/bin/sh", script}, argv[3:])
}

// An install script is confined like the server it installs: it can work in
// the server's directory, and cannot read the node token next door.
func TestNativeInstallScriptIsConfined(t *testing.T) {
	ip, root := newNativeInstallTestProcess(t, "true\n")
	ip.Script.Script = "cd /mnt/server && echo installed > marker\n" +
		"cat '" + filepath.Join(root, "config.yml") + "' > leaked 2>/dev/null\n" +
		"exit 0\n"
	enableSandboxForTest(t)

	require.NoError(t, ip.executeNative())

	dir := ip.Server.Filesystem().Path()
	body, err := os.ReadFile(filepath.Join(dir, "marker"))
	require.NoError(t, err, "the script could not write the server's own directory")
	assert.Equal(t, "installed\n", string(body))

	leaked, _ := os.ReadFile(filepath.Join(dir, "leaked"))
	assert.Empty(t, string(leaked), "the install script read wings' config.yml, which holds the node token")
}
