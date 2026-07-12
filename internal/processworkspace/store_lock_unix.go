//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package processworkspace

import (
	"errors"
	"os"
	"syscall"
)

func tryFlock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func lockUnavailable(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
