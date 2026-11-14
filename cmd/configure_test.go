package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pterodactyl/wings/config"
)

// The Panel emits Linux paths for every node regardless of what it runs, so on
// macOS a freshly fetched configuration has to be rewritten before it is usable.
func TestLocalizeDirectories(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("localizeDirectories only rewrites paths on darwin")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("no home directory: %v", err)
	}
	root := filepath.Join(home, "pterodactyl")

	t.Run("rewrites the panel's linux defaults", func(t *testing.T) {
		cfg := &config.Configuration{}
		cfg.System.RootDirectory = "/var/lib/pterodactyl"
		cfg.System.Data = "/var/lib/pterodactyl/volumes"
		cfg.System.LogDirectory = "/var/log/pterodactyl"
		cfg.System.ArchiveDirectory = "/var/lib/pterodactyl/archives"
		cfg.System.BackupDirectory = "/var/lib/pterodactyl/backups"
		cfg.System.TmpDirectory = "/tmp/pterodactyl"

		localizeDirectories(cfg)

		for _, c := range []struct{ got, want, name string }{
			{cfg.System.RootDirectory, root, "root_directory"},
			{cfg.System.Data, filepath.Join(root, "volumes"), "data"},
			{cfg.System.LogDirectory, filepath.Join(root, "logs"), "log_directory"},
			{cfg.System.ArchiveDirectory, filepath.Join(root, "archives"), "archive_directory"},
			{cfg.System.BackupDirectory, filepath.Join(root, "backups"), "backup_directory"},
			{cfg.System.TmpDirectory, filepath.Join(root, "tmp"), "tmp_directory"},
		} {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
			}
		}

		if !cfg.IgnorePanelConfigUpdates {
			t.Error("ignore_panel_config_updates = false, want true")
		}
	})

	t.Run("leaves a deliberate path alone", func(t *testing.T) {
		cfg := &config.Configuration{}
		cfg.System.RootDirectory = "/Users/someone/servers"
		cfg.System.Data = "/Volumes/External/volumes"

		localizeDirectories(cfg)

		if cfg.System.RootDirectory != "/Users/someone/servers" {
			t.Errorf("root_directory = %q, want it untouched", cfg.System.RootDirectory)
		}
		if cfg.System.Data != "/Volumes/External/volumes" {
			t.Errorf("data = %q, want it untouched", cfg.System.Data)
		}
	})

	t.Run("fills an empty path", func(t *testing.T) {
		cfg := &config.Configuration{}
		localizeDirectories(cfg)

		if cfg.System.Data != filepath.Join(root, "volumes") {
			t.Errorf("data = %q, want %q", cfg.System.Data, filepath.Join(root, "volumes"))
		}
	})
}

// useConfigureArgs points the configure command at a fake Panel and the given
// --config-path and --config files, restoring the package state afterwards.
func useConfigureArgs(t *testing.T, panelURL, configurePath, globalPath string) {
	t.Helper()
	prevArgs := configureArgs
	prevConfigPath := configPath
	t.Cleanup(func() {
		configureArgs = prevArgs
		configPath = prevConfigPath
	})

	configureArgs.PanelURL = panelURL
	configureArgs.Token = "test-token"
	configureArgs.Node = "1"
	configureArgs.ConfigPath = configurePath
	configureArgs.Override = false
	configureArgs.AllowInsecure = false
	configPath = globalPath
}

// newFakePanel serves the node configuration endpoint with the given body.
func newFakePanel(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/application/nodes/1/configuration" || r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clearTokenEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("WINGS_TOKEN_ID", "")
	t.Setenv("WINGS_TOKEN", "")
}

func TestConfigureWritesToConfigPath(t *testing.T) {
	clearTokenEnvironment(t)
	dir := t.TempDir()
	configurePath := filepath.Join(dir, "configure.yml")
	globalPath := filepath.Join(dir, "global.yml")
	const existing = "uuid: existing-node\n"
	require.NoError(t, os.WriteFile(globalPath, []byte(existing), 0o600))

	panel := newFakePanel(t, `{"uuid":"11111111-2222-3333-4444-555555555555","token_id":"panel-id","token":"panel-token"}`)
	useConfigureArgs(t, panel.URL, configurePath, globalPath)

	configureCmdRun(nil, nil)

	got, err := os.ReadFile(globalPath)
	require.NoError(t, err)
	require.Equal(t, existing, string(got), "the --config file should not be written when --config-path is given")

	cfg, err := config.Load(configurePath)
	require.NoError(t, err)
	require.Equal(t, "panel-token", cfg.AuthenticationToken)
	require.Equal(t, panel.URL, cfg.PanelLocation)
}

// The token from the Panel is used as it is.
func TestConfigureRequiresTokenValue(t *testing.T) {
	clearTokenEnvironment(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("file-token"), 0o600))

	for _, token := range []string{"file://" + tokenFile, "$HOME"} {
		path := filepath.Join(t.TempDir(), "config.yml")
		body := `{"token_id":"panel-id","token":"` + token + `"}`

		require.Error(t, writePanelConfiguration(path, "http://panel.test", []byte(body)), token)
		require.NoFileExists(t, path)
	}
}

// Configuring an existing node keeps the settings the Panel does not manage.
func TestConfigureKeepsExistingSettings(t *testing.T) {
	clearTokenEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`token_id: old-id
token: old-token
allowed_mounts:
  - /srv/shared
docker:
  registries:
    registry.example.com:
      username: user
      password: pass
`), 0o600))

	require.NoError(t, writePanelConfiguration(path, "http://panel.test", []byte(`{"token_id":"new-id","token":"new-token"}`)))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, "new-id", cfg.AuthenticationTokenId)
	require.Equal(t, "new-token", cfg.AuthenticationToken)
	require.Equal(t, []string{"/srv/shared"}, cfg.AllowedMounts)
	require.Contains(t, cfg.Docker.Registries, "registry.example.com")
}

// Configuring an existing node replaces the mounts with the ones from the Panel
// and keeps the local data directory.
func TestConfigureAppliesPanelMounts(t *testing.T) {
	clearTokenEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`token_id: old-id
token: old-token
allowed_mounts:
  - /srv/shared
system:
  data: /srv/old-volumes
`), 0o600))

	body := `{"token_id":"new-id","token":"new-token","allowed_mounts":["/srv/media","/srv/maps"],"system":{"data":"/srv/volumes","sftp":{"bind_port":2022}}}`
	require.NoError(t, writePanelConfiguration(path, "http://panel.test", []byte(body)))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, []string{"/srv/media", "/srv/maps"}, cfg.AllowedMounts)
	require.Equal(t, "/srv/old-volumes", cfg.System.Data)
}

// Mounts removed from the node in the Panel are removed from the configuration.
func TestConfigureClearsRemovedMounts(t *testing.T) {
	clearTokenEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`token_id: old-id
token: old-token
allowed_mounts:
  - /srv/shared
`), 0o600))

	require.NoError(t, writePanelConfiguration(path, "http://panel.test", []byte(`{"token_id":"new-id","token":"new-token","allowed_mounts":[]}`)))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.Empty(t, cfg.AllowedMounts)
}

// Configure leaves a node with directories it can use: the Linux defaults stay
// as they are everywhere else, and on macOS, where an unprivileged wings cannot
// write to /var, they move under the user's data directory.
func TestConfigureLocalizesDirectories(t *testing.T) {
	clearTokenEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "pterodactyl")
	const body = `{"token_id":"new-id","token":"new-token","system":{"data":"/srv/volumes"}}`

	t.Run("new configuration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yml")
		require.NoError(t, writePanelConfiguration(path, "http://panel.test", []byte(body)))

		cfg, err := config.Load(path)
		require.NoError(t, err)
		require.Equal(t, "new-token", cfg.AuthenticationToken)

		if runtime.GOOS != "darwin" {
			require.Equal(t, "/var/lib/pterodactyl", cfg.System.RootDirectory)
			require.Equal(t, "/var/lib/pterodactyl/volumes", cfg.System.Data)
			require.Equal(t, "/var/log/pterodactyl", cfg.System.LogDirectory)
			require.Equal(t, "/tmp/pterodactyl", cfg.System.TmpDirectory)
			require.False(t, cfg.IgnorePanelConfigUpdates)
			return
		}

		require.Equal(t, root, cfg.System.RootDirectory)
		require.Equal(t, filepath.Join(root, "volumes"), cfg.System.Data)
		require.Equal(t, filepath.Join(root, "logs"), cfg.System.LogDirectory)
		require.Equal(t, filepath.Join(root, "archives"), cfg.System.ArchiveDirectory)
		require.Equal(t, filepath.Join(root, "backups"), cfg.System.BackupDirectory)
		require.Equal(t, filepath.Join(root, "tmp"), cfg.System.TmpDirectory)
		require.True(t, cfg.IgnorePanelConfigUpdates)
	})

	// A file copied by hand from the Panel still has its Linux paths; a path the
	// operator chose is kept either way.
	t.Run("existing configuration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yml")
		require.NoError(t, os.WriteFile(path, []byte(`token_id: old-id
token: old-token
system:
  root_directory: /Users/someone/servers
  data: /var/lib/pterodactyl/volumes
`), 0o600))

		require.NoError(t, writePanelConfiguration(path, "http://panel.test", []byte(body)))

		cfg, err := config.Load(path)
		require.NoError(t, err)
		require.Equal(t, "new-token", cfg.AuthenticationToken)
		require.Equal(t, "/Users/someone/servers", cfg.System.RootDirectory)

		if runtime.GOOS != "darwin" {
			require.Equal(t, "/var/lib/pterodactyl/volumes", cfg.System.Data)
			return
		}
		require.Equal(t, filepath.Join(root, "volumes"), cfg.System.Data)
	})
}
