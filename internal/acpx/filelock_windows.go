//go:build windows

package acpx

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const overrideLockByteCount = 1

func tryLockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, overrideLockByteCount, 0, &overlapped)
}

func unlockOverrideFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, overrideLockByteCount, 0, &overlapped)
}

func lockUnavailable(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
