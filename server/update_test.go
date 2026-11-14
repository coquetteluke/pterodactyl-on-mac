package server

import (
	"testing"

	"github.com/pterodactyl/wings/config"
	"github.com/pterodactyl/wings/environment"
	"github.com/pterodactyl/wings/environment/docker"
	"github.com/pterodactyl/wings/environment/native"
	"github.com/pterodactyl/wings/remote"
	"github.com/pterodactyl/wings/server/filesystem"
)

// Both environments have to take the stop configuration. If one stops doing so
// it no longer learns how its servers are meant to be stopped, and this fails
// to compile rather than leaving that to be found out on a running node.
var (
	_ stopConfigurable = (*docker.Environment)(nil)
	_ stopConfigurable = (*native.Environment)(nil)
)

// stopRecordingEnvironment is an environment that is not Docker's and records
// the image and stop configuration it is given.
type stopRecordingEnvironment struct {
	environment.ProcessEnvironment
	cfg   *environment.Configuration
	image string
	stop  remote.ProcessStopConfiguration
}

func (e *stopRecordingEnvironment) Config() *environment.Configuration { return e.cfg }
func (e *stopRecordingEnvironment) InSituUpdate() error                { return nil }
func (e *stopRecordingEnvironment) State() string                      { return environment.ProcessOfflineState }
func (e *stopRecordingEnvironment) SetImage(i string)                  { e.image = i }
func (e *stopRecordingEnvironment) SetStopConfiguration(c remote.ProcessStopConfiguration) {
	e.stop = c
}

// The stop configuration is passed to whichever environment runs the server,
// not only to Docker's. An environment that never receives it cannot tell a
// stop the user typed into the console from a crash, and restarts the server.
func TestSyncWithEnvironmentPassesStopConfigurationToAnyEnvironment(t *testing.T) {
	config.Set(&config.Configuration{AuthenticationToken: "abc"})

	s, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.fs, err = filesystem.New(t.TempDir(), 0, nil, filesystem.Owner{})
	if err != nil {
		t.Fatal(err)
	}
	env := &stopRecordingEnvironment{cfg: environment.NewConfiguration(environment.Settings{}, nil)}
	s.Environment = env
	s.Config().Container.Image = "ghcr.io/example/java:21"
	s.procConfig = &remote.ProcessConfiguration{}
	s.procConfig.Stop.Type = remote.ProcessStopCommand
	s.procConfig.Stop.Value = "stop"

	s.SyncWithEnvironment()

	if env.stop.Type != remote.ProcessStopCommand || env.stop.Value != "stop" {
		t.Fatalf("expected the stop configuration to reach the environment, got %+v", env.stop)
	}
	if env.image != "ghcr.io/example/java:21" {
		t.Fatalf("expected the image to reach the environment, got %q", env.image)
	}
}
