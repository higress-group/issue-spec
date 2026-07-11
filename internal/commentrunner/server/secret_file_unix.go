//go:build !windows

package server

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openPrivateFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open private file %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat private file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		file.Close()
		return nil, fmt.Errorf("private file %s must be a regular file with mode 0600", path)
	}
	return file, nil
}
