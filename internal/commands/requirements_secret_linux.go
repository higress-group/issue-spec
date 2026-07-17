//go:build linux

package commands

import (
	"bufio"
	"errors"
	"io"
	"os"
	"syscall"
	"unsafe"
)

func readHiddenRequirementsSecret(input io.Reader, output io.Writer) (string, error) {
	file, ok := input.(*os.File)
	if !ok {
		return "", errors.New("interactive PAT input requires a terminal; use --token-stdin for explicit protected stdin")
	}
	fd := file.Fd()
	var previous syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&previous)), 0, 0, 0); errno != 0 {
		return "", errors.New("interactive PAT input requires a terminal; use --token-stdin for explicit protected stdin")
	}
	hidden := previous
	hidden.Lflag &^= syscall.ECHO
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&hidden)), 0, 0, 0); errno != 0 {
		return "", errors.New("disable terminal echo for PAT input")
	}
	defer syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&previous)), 0, 0, 0)
	_, _ = io.WriteString(output, "PAT: ")
	line, err := bufio.NewReader(file).ReadString('\n')
	_, _ = io.WriteString(output, "\n")
	return line, err
}
