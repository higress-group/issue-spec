package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// Raw state backup and legacy evidence.
//
// The first applied migration must preserve the raw pre-Normalize state bytes
// before any current-binary save rewrites Acpx.CWD with Workspace.Path. The
// backup is the only original-path evidence source for legacy ownership proof;
// normal LoadFile output is never original-path evidence.

const (
	rawStateBackupName  = "state-first.json"
	sidecarBackupPrefix = "sidecar-first"
)

// EnsureRawStateBackup atomically preserves the first raw state bytes under
// `.storage/backups/`. It is idempotent and never overwrites the original.
// A missing state file is a no-op so fresh runners are unaffected.
func EnsureRawStateBackup(workspaceRoot, statePath string) (string, error) {
	statePath = strings.TrimSpace(statePath)
	if statePath == "" {
		return "", nil
	}
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read raw state for backup: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return "", err
	}
	backupDir := filepath.Join(canonical, StorageDirName, backupDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	backup := filepath.Join(backupDir, rawStateBackupName)
	if _, err := os.Lstat(backup); err == nil {
		return backup, nil
	}
	if err := state.WriteAtomic(backup, data); err != nil {
		return "", fmt.Errorf("write raw state backup: %w", err)
	}
	return backup, nil
}

// LoadLegacyEvidence parses raw pre-Normalize state bytes and extracts the
// original Acpx.CWD candidates per session key. It accepts the current typed
// cwd field as stored on disk and the legacy flattened raw.cwd form. Corrupt
// input yields no evidence, never an error that broadens deletion.
func LoadLegacyEvidence(rawStatePath string) (map[string][]string, error) {
	evidence := map[string][]string{}
	data, err := os.ReadFile(rawStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return evidence, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read raw state evidence: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return evidence, nil
	}
	var doc struct {
		PublicSessions map[string]struct {
			Acpx struct {
				CWD string            `json:"cwd"`
				Raw map[string]string `json:"raw"`
			} `json:"acpx"`
		} `json:"public_sessions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse raw state evidence: %w", err)
	}
	for key, session := range doc.PublicSessions {
		cwd := strings.TrimSpace(session.Acpx.CWD)
		if cwd == "" {
			cwd = strings.TrimSpace(session.Acpx.Raw["cwd"])
		}
		if cwd == "" {
			continue
		}
		evidence[key] = appendUniqueString(evidence[key], cwd)
	}
	return evidence, nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
