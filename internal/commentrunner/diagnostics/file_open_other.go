//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package diagnostics

import "os"

// Platforms without a portable O_NOFOLLOW equivalent use a non-mutating open
// followed by Lstat/File.Stat identity checks in path.go. Append mode does not
// write until that validation succeeds, and every later write revalidates the
// path against the open handle.
func openAppendFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, DefaultFileMode)
}

func openExistingFileNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
