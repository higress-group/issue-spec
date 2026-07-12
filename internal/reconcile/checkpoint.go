package reconcile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Checkpoint struct {
	Version    int               `json:"version"`
	PlanDigest string            `json:"plan_digest"`
	Completed  map[string]string `json:"completed"`
}

func LoadCheckpoint(path, digest string) (Checkpoint, error) {
	cp := Checkpoint{Version: 1, PlanDigest: digest, Completed: map[string]string{}}
	if path == "" {
		return cp, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cp, nil
	}
	if err != nil {
		return Checkpoint{}, err
	}
	if err := json.Unmarshal(data, &cp); err != nil {
		return Checkpoint{}, fmt.Errorf("corrupted checkpoint: %w", err)
	}
	if cp.Version != 1 || cp.PlanDigest != digest || cp.Completed == nil {
		return Checkpoint{}, fmt.Errorf("checkpoint does not match plan digest %s", digest)
	}
	return cp, nil
}

func SaveCheckpoint(path string, cp Checkpoint) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".issue-spec-checkpoint-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
