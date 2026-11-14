package server

import (
	"fmt"
	"path/filepath"

	"github.com/apex/log"

	"github.com/pterodactyl/wings/config"
)

// skipCustomMounts reports whether this node runs its servers natively, in
// which case none of a server's custom mounts are applied: there is no
// container to bind them into, and a process on the host sees the host's own
// paths.
//
// Each mount is reported in the server console, the way one that fails
// validation is. Left to customMounts, a mount that passes validation would be
// handed to an environment that ignores it without a word, and one that fails
// would be reported in terms that suggest correcting its path would mount it.
//
// This is called from Mounts rather than customMounts so that upstream's tests
// of the validation, which call customMounts directly, still mean something on
// macOS, where the native environment is the default.
func (s *Server) skipCustomMounts() bool {
	if !config.UseNativeEnvironment() {
		return false
	}
	for _, m := range s.Config().Mounts {
		target := filepath.Clean(m.Target)
		s.Log().WithFields(log.Fields{
			"source_path": filepath.Clean(m.Source),
			"target_path": target,
		}).Warn("skipping custom server mount, the native environment does not apply mounts")
		// The source is left out, as it is a path on the host.
		s.PublishConsoleOutputFromDaemon(fmt.Sprintf("Skipped the mount for %s, mounts are not supported on this node.", target))
	}
	return true
}
