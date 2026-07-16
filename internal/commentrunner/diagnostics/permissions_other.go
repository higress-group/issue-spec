//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package diagnostics

import "os"

// Windows and the remaining Go targets do not all expose POSIX permission
// semantics. Keep Chmod as a best-effort tightening step, but rely on the
// platform ACL and the path identity/type checks rather than rejecting a file
// because Stat cannot report an exact 0600/0700 mode.
func tightenDiagnosticPermissions(file *os.File, mode os.FileMode) error {
	return file.Chmod(mode)
}

func diagnosticPermissionsMatch(_ os.FileInfo, _ os.FileMode) bool {
	return true
}
