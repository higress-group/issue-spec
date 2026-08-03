//go:build windows

package storage

import "testing"

func TestStatfsFreeBytesReturnsLiveValue(t *testing.T) {
	free, err := statfsFreeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("statfsFreeBytes: %v", err)
	}
	if free == 0 {
		t.Fatalf("statfsFreeBytes returned 0 for a writable temp filesystem")
	}
}
