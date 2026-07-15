//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package diagnostics

import "os"

func tightenDiagnosticPermissions(file *os.File, mode os.FileMode) error {
	return file.Chmod(mode)
}

func diagnosticPermissionsMatch(info os.FileInfo, mode os.FileMode) bool {
	return info.Mode().Perm() == mode.Perm()
}
