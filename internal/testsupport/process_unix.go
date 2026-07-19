//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package testsupport

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureProcAttr places the child in its own process group so the entire
// process tree can be signalled together via a negative PGID.
func configureProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcess kills the child's whole process group when possible so that
// grandchildren are terminated as well. It falls back to killing just the direct
// child if the process group cannot be resolved.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
			return nil
		}
	}
	return cmd.Process.Kill()
}

// pidAlive reports whether a process with the given PID is currently alive. A
// signal-0 probe returns nil for a live process we own and EPERM for a live
// process owned by another user; ESRCH indicates the process is gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
