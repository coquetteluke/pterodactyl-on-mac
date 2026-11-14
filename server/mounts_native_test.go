package server

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/events"
	"github.com/pterodactyl/wings/server/filesystem"
)

// mountTestServer returns a server with a data directory and a single custom
// mount onto /data, on a node that runs the named environment.
func mountTestServer(t *testing.T, env string, source string) *Server {
	t.Helper()
	config.Update(func(c *config.Configuration) { c.System.Environment = env })
	t.Cleanup(func() {
		config.Update(func(c *config.Configuration) { c.System.Environment = "auto" })
	})

	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.fs, err = filesystem.New(t.TempDir(), 0, nil, filesystem.Owner{})
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Mounts = []Mount{{Source: source, Target: "/data"}}
	return s
}

// dataMountSource returns the source of the mount onto /data that Mounts
// returns for the server, or an empty string if there is none.
func dataMountSource(s *Server) string {
	for _, m := range s.Mounts() {
		if m.Target == "/data" {
			return m.Source
		}
	}
	return ""
}

// The native environment never applies a custom mount. One that would pass
// validation used to be dropped without a word, so it is reported the way a
// skipped one is, and is not handed to the environment.
func TestNativeEnvironmentReportsCustomMountsAsSkipped(t *testing.T) {
	root := setupMountTest(t, "mnt")
	mkdirs(t, root, "mnt/maps")
	s := mountTestServer(t, "native", filepath.Join(root, "mnt", "maps"))
	ch := make(chan []byte, 8)
	s.Events().On(ch)

	if got := dataMountSource(s); got != "" {
		t.Fatalf("expected the custom mount to be left out, got %s", got)
	}

	select {
	case b := <-ch:
		var e events.Event
		if err := events.DecodeTo(b, &e); err != nil {
			t.Fatal(err)
		}
		msg, _ := e.Data.(string)
		if e.Topic != ConsoleOutputEvent || !strings.Contains(msg, "/data") || !strings.Contains(msg, "not supported") {
			t.Fatalf("expected a console message saying the /data mount is not supported, got %s %q", e.Topic, msg)
		}
		if strings.Contains(msg, root) {
			t.Fatalf("expected the console message to not include host paths, got %q", msg)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("expected a console message about the skipped mount")
	}
}

// Docker still gets a mount that passes validation, and nothing is said of it.
func TestDockerEnvironmentKeepsCustomMounts(t *testing.T) {
	root := setupMountTest(t, "mnt")
	mkdirs(t, root, "mnt/maps")
	source := filepath.Join(root, "mnt", "maps")
	s := mountTestServer(t, "docker", source)
	ch := make(chan []byte, 8)
	s.Events().On(ch)

	if got := dataMountSource(s); got != source {
		t.Fatalf("expected the custom mount to be passed on as %s, got %q", source, got)
	}

	select {
	case b := <-ch:
		t.Fatalf("expected no console message for a mount that is applied, got %s", b)
	default:
		// Publishing waits for the event to be handed to every listener, so
		// one would be here by now.
	}
}
