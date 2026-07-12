//go:build windows

package credentials

import "os"

func singleLink(os.FileInfo) bool { return false }
