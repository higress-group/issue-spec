//go:build windows

package codereview

import "os"

// Windows access is governed by the file ACL rather than POSIX permission
// bits. Regular-file/symlink/identity checks remain enforced by the shared
// loader; deployment must grant the CLI identity exclusive read access.
func operatorFileIsPrivate(os.FileInfo) bool { return true }
