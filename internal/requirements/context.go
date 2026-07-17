package requirements

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
)

const activeContextFileName = "requirements-context.json"

var ErrContextNotConfigured = errors.New("requirements context is not configured")

// ActiveContext is the single non-secret requirements workflow selection.
// Credentials and endpoints stay in the origin-bound auth profile.
type ActiveContext struct {
	Profile          string `json:"profile"`
	ServerInstanceID string `json:"server_instance_id"`
	Repository       string `json:"repository"`
	Agent            Target `json:"agent"`
}

func (c ActiveContext) Validate() error {
	if strings.TrimSpace(c.Profile) == "" || strings.ContainsAny(c.Profile, "\\/\r\n\t\x00") {
		return errors.New("requirements context profile is invalid")
	}
	if strings.TrimSpace(c.ServerInstanceID) == "" || strings.ContainsAny(c.ServerInstanceID, "\r\n\x00") {
		return errors.New("requirements context server instance id is invalid")
	}
	if !validRepositoryName(c.Repository) {
		return errors.New("requirements context repository must be owner/name")
	}
	if c.Agent != TargetCodex && c.Agent != TargetClaude {
		return fmt.Errorf("requirements context agent must be %s or %s", TargetCodex, TargetClaude)
	}
	return nil
}

func validRepositoryName(value string) bool {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\\\r\n\t\x00 ") {
		return false
	}
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && parts[0] != "." && parts[1] != "." && parts[0] != ".." && parts[1] != ".."
}

func ContextPath() (string, error) {
	dir, err := auth.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, activeContextFileName), nil
}

// LoadActiveContext rejects permissive, linked, oversized, or ambiguous
// context files before using their profile and repository selection.
func LoadActiveContext() (ActiveContext, error) {
	path, err := ContextPath()
	if err != nil {
		return ActiveContext{}, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ActiveContext{}, ErrContextNotConfigured
	}
	if err != nil {
		return ActiveContext{}, fmt.Errorf("inspect requirements context: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 64<<10 {
		return ActiveContext{}, errors.New("requirements context must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return ActiveContext{}, fmt.Errorf("open requirements context: %w", err)
	}
	defer file.Close()
	var context ActiveContext
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&context); err != nil {
		return ActiveContext{}, fmt.Errorf("decode requirements context: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ActiveContext{}, errors.New("decode requirements context: trailing JSON content")
	}
	if err := context.Validate(); err != nil {
		return ActiveContext{}, err
	}
	return context, nil
}

// SaveActiveContext atomically installs one owner-only context. Equal content
// is a no-op so setup can be rerun safely after any later step fails.
func SaveActiveContext(context ActiveContext) (bool, error) {
	context.Profile = strings.TrimSpace(context.Profile)
	context.ServerInstanceID = strings.TrimSpace(context.ServerInstanceID)
	context.Repository = strings.TrimSpace(context.Repository)
	if err := context.Validate(); err != nil {
		return false, err
	}
	path, err := ContextPath()
	if err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(context, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, data) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return false, errors.New("requirements context must be a private regular file")
		}
		return false, nil
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return false, fmt.Errorf("read existing requirements context: %w", readErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create requirements config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".requirements-context-*")
	if err != nil {
		return false, fmt.Errorf("create requirements context: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return false, fmt.Errorf("activate requirements context: %w", err)
	}
	return true, nil
}
