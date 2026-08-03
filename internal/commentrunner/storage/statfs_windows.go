//go:build windows

package storage

import (
	"golang.org/x/sys/windows"
)

// statfsFreeBytes returns live free bytes available to the current user on the
// volume containing path (quota-aware, matching the unix Bavail semantics).
func statfsFreeBytes(path string) (uint64, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var freeAvailable, totalBytes, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(name, &freeAvailable, &totalBytes, &totalFree); err != nil {
		return 0, err
	}
	return freeAvailable, nil
}
