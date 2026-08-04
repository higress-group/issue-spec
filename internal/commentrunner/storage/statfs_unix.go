//go:build linux || darwin || dragonfly

package storage

import "golang.org/x/sys/unix"

// statfsFreeBytes returns live free bytes available to unprivileged writers.
func statfsFreeBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bavail <= 0 || stat.Bsize <= 0 {
		return 0, nil
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
