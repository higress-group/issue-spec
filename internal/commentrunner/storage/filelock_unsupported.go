//go:build !windows && !unix

package storage

import (
	"errors"
	"os"
)

var errFlockUnsupported = errors.New("file locking unsupported on this platform")

func flockExclusive(*os.File) error    { return errFlockUnsupported }
func flockTryExclusive(*os.File) error { return errFlockUnsupported }
func flockUnlock(*os.File) error       { return nil }
func lockWouldBlock(error) bool        { return false }
