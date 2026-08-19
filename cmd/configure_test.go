package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

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
