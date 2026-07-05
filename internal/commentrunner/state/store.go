package state

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrLocked  = errors.New("runner state store locked")
	ErrCorrupt = errors.New("runner state file corrupt")
)

type StateStore interface {
	Load(context.Context) (RunnerState, error)
	Save(context.Context, RunnerState) error
	Update(context.Context, func(*RunnerState) error) error
	Close() error
}

type FileStore struct {
	mu       sync.Mutex
	path     string
	lockPath string
	lockFile *os.File
	closed   bool
}

type LockError struct {
	Path   string
	Holder string
}

func (e *LockError) Error() string {
	if e.Holder == "" {
		return fmt.Sprintf("%s: %v", e.Path, ErrLocked)
	}
	return fmt.Sprintf("%s: %v: %s", e.Path, ErrLocked, e.Holder)
}

func (e *LockError) Is(target error) bool {
	return target == ErrLocked
}

type CorruptStateError struct {
	Path string
	Err  error
}

func (e *CorruptStateError) Error() string {
	return fmt.Sprintf("%s: %v: %v", e.Path, ErrCorrupt, e.Err)
}

func (e *CorruptStateError) Unwrap() error {
	return e.Err
}

func (e *CorruptStateError) Is(target error) bool {
	return target == ErrCorrupt
}

func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("state path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return nil, err
	}
	lockPath := absPath + ".lock"
	lockFile, err := acquireLock(lockPath, absPath)
	if err != nil {
		return nil, err
	}
	return &FileStore{path: absPath, lockPath: lockPath, lockFile: lockFile}, nil
}

func (s *FileStore) Load(ctx context.Context) (RunnerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpen(); err != nil {
		return RunnerState{}, err
	}
	if err := checkContext(ctx); err != nil {
		return RunnerState{}, err
	}
	return LoadFile(s.path)
}

func (s *FileStore) Save(ctx context.Context, state RunnerState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return SaveFile(s.path, state)
}

func (s *FileStore) Update(ctx context.Context, mutate func(*RunnerState) error) error {
	if mutate == nil {
		return fmt.Errorf("state update callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureOpen(); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	state, err := LoadFile(s.path)
	if err != nil {
		return err
	}
	if err := mutate(&state); err != nil {
		return err
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	return SaveFile(s.path, state)
}

func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var closeErr, removeErr error
	if s.lockFile != nil {
		same, err := sameOpenFilePath(s.lockFile, s.lockPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr = err
		} else if same {
			removeErr = os.Remove(s.lockPath)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
		}
		closeErr = s.lockFile.Close()
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func LoadFile(path string) (RunnerState, error) {
	state := NewState()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return RunnerState{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return RunnerState{}, &CorruptStateError{Path: path, Err: fmt.Errorf("empty state file")}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&state); err != nil {
		return RunnerState{}, &CorruptStateError{Path: path, Err: err}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return RunnerState{}, &CorruptStateError{Path: path, Err: err}
	}
	state.Normalize()
	return state, nil
}

func SaveFile(path string, state RunnerState) error {
	if path == "" {
		return fmt.Errorf("state path is required")
	}
	now := time.Now().UTC()
	state.Normalize()
	// Keep state.json bounded automatically: tombstone terminal records and
	// prune aged/over-cap ones on every save (Compact re-normalizes internally).
	state.Compact(now, DefaultRetentionPolicy())
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	state.UpdatedAt = now
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(path, data)
}

func (s *FileStore) ensureOpen() error {
	if s == nil || s.closed {
		return fmt.Errorf("state store is closed")
	}
	return nil
}

func acquireLock(lockPath, statePath string) (*os.File, error) {
	lockFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := tryLockFile(lockFile); err != nil {
		holder, _ := os.ReadFile(lockPath)
		_ = lockFile.Close()
		if lockUnavailable(err) {
			return nil, &LockError{Path: lockPath, Holder: string(bytes.TrimSpace(holder))}
		}
		return nil, err
	}
	if err := writeStateLock(lockFile, statePath); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return lockFile, nil
}

func writeStateLock(lockFile *os.File, statePath string) error {
	if err := lockFile.Truncate(0); err != nil {
		return err
	}
	if _, err := lockFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(lockFile, "pid=%d\ncreated_at=%s\nstate_path=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), statePath); err != nil {
		return err
	}
	return lockFile.Sync()
}

func sameOpenFilePath(file *os.File, path string) (bool, error) {
	if file == nil {
		return false, nil
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return false, err
	}
	pathInfo, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(fileInfo, pathInfo), nil
}

func writeAtomic(path string, data []byte) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(absPath)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			os.Remove(tmpName)
		}
	}()
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
	if err := os.Rename(tmpName, absPath); err != nil {
		return err
	}
	cleanup = false
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
