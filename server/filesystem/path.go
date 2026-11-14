package filesystem

import (
	"path"
	"path/filepath"
	"strings"

	"emperror.dev/errors"
)

// Checks if the given file or path is in the server's file denylist. If so, an Error
// is returned, otherwise nil is returned.
func (fs *Filesystem) IsIgnored(paths ...string) error {
	for _, p := range paths {
		//sp, err := fs.SafePath(p)
		//if err != nil {
		//	return err
		//}
		// TODO: update logic to use unixFS
		//
		// Match against the cleaned path the filesystem will use. A trailing slash is
		// kept so that it still matches patterns for directories.
		cleaned := path.Clean("/" + filepath.ToSlash(p))
		if strings.HasSuffix(p, "/") && cleaned != "/" {
			cleaned += "/"
		}
		// The denylist is lowercased on case-insensitive volumes, so the path
		// has to be as well or "Config.yml" walks straight past "config.yml".
		if foldPathCase {
			cleaned = strings.ToLower(cleaned)
		}
		if fs.denylist.MatchesPath(cleaned) {
			return errors.WithStack(&Error{code: ErrCodeDenylistFile, path: p, resolved: p})
		}
	}
	return nil
}
