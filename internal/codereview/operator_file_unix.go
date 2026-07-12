//go:build !windows

package codereview

import (
	"os"
	"syscall"
)

func operatorFileIsPrivate(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && info.Mode().Perm()&0o077 == 0
}
