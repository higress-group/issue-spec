package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
)

type SecretReference struct {
	File string
	Env  string
}

func (r SecretReference) Load() ([]byte, error) {
	file, env := strings.TrimSpace(r.File), strings.TrimSpace(r.Env)
	if (file == "") == (env == "") {
		return nil, errors.New("secret reference must select exactly one file or environment variable")
	}
	if env != "" {
		value, ok := os.LookupEnv(env)
		if !ok || value == "" {
			return nil, fmt.Errorf("secret environment variable %s is not set", env)
		}
		if err := os.Unsetenv(env); err != nil {
			return nil, fmt.Errorf("clear secret environment variable %s: %w", env, err)
		}
		if len(value) > webhook.MaxSecretBytes {
			return nil, fmt.Errorf("secret environment variable %s exceeds %d bytes", env, webhook.MaxSecretBytes)
		}
		return []byte(value), nil
	}
	handle, err := openPrivateFile(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	value, err := io.ReadAll(io.LimitReader(handle, webhook.MaxSecretBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secret file %s: %w", file, err)
	}
	value = trimSingleLineEnding(value)
	if len(value) == 0 {
		return nil, fmt.Errorf("secret file %s is empty", file)
	}
	if len(value) > webhook.MaxSecretBytes {
		return nil, fmt.Errorf("secret file %s exceeds %d bytes", file, webhook.MaxSecretBytes)
	}
	return value, nil
}

func ValidatePrivateFile(file string) error {
	handle, err := openPrivateFile(file)
	if err != nil {
		return err
	}
	return handle.Close()
}

func trimSingleLineEnding(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	return value
}
