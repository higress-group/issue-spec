//go:build !windows

package auth

import (
	"os"
	"syscall"
)

func singleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
