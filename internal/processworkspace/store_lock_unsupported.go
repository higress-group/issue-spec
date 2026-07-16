//go:build !windows && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package processworkspace

import (
	"errors"
	"os"
)

var errRegistryLockUnsupported = errors.New("process workspace registry file locking is unsupported on this platform")

func tryFlock(*os.File) error    { return errRegistryLockUnsupported }
func unlockFile(*os.File) error  { return errRegistryLockUnsupported }
func lockUnavailable(error) bool { return false }
