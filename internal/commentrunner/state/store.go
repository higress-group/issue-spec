package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
)

type Store struct {
	path string
}

func New(path string) *Store { return &Store{path: path} }

func (s *Store) Load() (model.RunnerState, error) {
	var st model.RunnerState
	data, err := os.ReadFile(s.path)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return model.RunnerState{}, fmt.Errorf("decode runner state %s: %w", s.path, err)
	}
	if st.Repos == nil {
		st.Repos = map[string]*model.RepoState{}
	}
	return st, nil
}

func (s *Store) Save(st model.RunnerState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "."+filepath.Base(s.path)+".*.tmp")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(st); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func (s *Store) WithLock(fn func() error) error {
	lockPath := s.path + ".lock"
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("runner state lock busy: %s", lockPath)
		}
		return err
	}
}

func (s *Store) Update(fn func(*model.RunnerState) error) error {
	return s.WithLock(func() error {
		st, err := s.Load()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if errors.Is(err, os.ErrNotExist) {
			st = model.RunnerState{Repos: map[string]*model.RepoState{}}
		}
		if st.Repos == nil {
			st.Repos = map[string]*model.RepoState{}
		}
		if err := fn(&st); err != nil {
			return err
		}
		return s.Save(st)
	})
}

func RepoKey(owner, repo string) string {
	return strings.TrimSpace(owner) + "/" + strings.TrimSpace(repo)
}

func PublicSessionKey(repoID, publicSessionID string) string {
	return strings.TrimSpace(repoID) + ":" + strings.TrimSpace(publicSessionID)
}

func LookupIdempotency(rs *model.RepoState, key string) (model.IdempotencyRecord, bool) {
	if rs == nil || rs.Idempotency == nil {
		return model.IdempotencyRecord{}, false
	}
	rec, ok := rs.Idempotency[strings.TrimSpace(key)]
	return rec, ok
}

func EnsureRepo(st *model.RunnerState, repoKey string) *model.RepoState {
	if st.Repos == nil {
		st.Repos = map[string]*model.RepoState{}
	}
	rs := st.Repos[repoKey]
	if rs == nil {
		rs = &model.RepoState{
			FirstObservedComments: map[string]model.ObservedComment{},
			Jobs:                  map[string]*model.JobState{},
			PublicSessions:        map[string]*model.PublicSessionState{},
			Workspaces:            map[string]*model.WorkspaceState{},
			Sandboxes:             map[string]*model.SandboxState{},
			Acpx:                  map[string]*model.AcpxState{},
			StatusComments:        map[string]*model.StatusCommentState{},
			CLIProvenance:         map[string]*model.CLIProvenanceState{},
			Idempotency:           map[string]model.IdempotencyRecord{},
			Cancellation:          map[string]model.CancellationState{},
		}
		st.Repos[repoKey] = rs
	}
	return rs
}
