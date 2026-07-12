//go:build windows

package server

import (
	"fmt"
	"os"
)

func openPrivateFile(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private file %s must be a non-symlink file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open private file %s: %w", path, err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm() != 0o600 {
		file.Close()
		return nil, fmt.Errorf("private file %s must be a regular file with mode 0600", path)
	}
	return file, nil
}
