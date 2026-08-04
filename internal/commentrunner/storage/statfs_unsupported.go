//go:build !linux && !darwin && !dragonfly && !freebsd && !windows

package storage

import "fmt"

func statfsFreeBytes(string) (uint64, error) {
	return 0, fmt.Errorf("filesystem free-space admission is unsupported on this platform")
}
