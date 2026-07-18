//go:build !linux

package commands

import (
	"errors"
	"io"
	"os"
)

func readHiddenRequirementsSecret(io.Reader, io.Writer) (string, error) {
	return "", errors.New("interactive hidden PAT input is unavailable on this platform; use --token-stdin for explicit protected stdin")
}

func requirementsInputIsTerminal(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
