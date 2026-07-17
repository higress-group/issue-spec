//go:build !linux

package commands

import (
	"errors"
	"io"
)

func readHiddenRequirementsSecret(io.Reader, io.Writer) (string, error) {
	return "", errors.New("interactive hidden PAT input is unavailable on this platform; use --token-stdin for explicit protected stdin")
}
