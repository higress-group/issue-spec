//go:build windows

package auth

import "os"

func singleLink(os.FileInfo) bool { return false }
