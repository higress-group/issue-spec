//go:build windows

package sandbox

import "os"

// Credential file capabilities are unsupported without the Linux sandbox.
// Fail closed before accepting a source whose link count cannot be proven.
func singleLink(os.FileInfo) bool { return false }
