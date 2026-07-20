//go:build windows

package testsupport

import "os/exec"

// configureProcAttr is a documented no-op on Windows. There is no direct
// equivalent of a Unix process group here; a full implementation would attach
// the child to a Job object. The fallback below terminates the direct child
// instead, which is sufficient for the single-level children the harness spawns.
func configureProcAttr(cmd *exec.Cmd) {}

// terminateProcess kills the direct child process. Grandchildren are not
// guaranteed to be reaped on Windows without a Job object; this is the
// documented fallback behavior.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// pidAlive is conservative on Windows: without a reliable, dependency-free
// liveness probe it reports every non-trivial PID as alive so that stale
// recovery retains uncertain candidates rather than removing them.
func pidAlive(pid int) bool {
	return pid > 0
}
