package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Store manages the log file structure and lifecycle
type Store struct {
	config   Config
	redactor *Redactor
}

// NewStore creates a new log store
func NewStore(config Config, redactor *Redactor) (*Store, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Ensure log directory exists
	if err := os.MkdirAll(config.LogDir, DefaultDirMode); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	// Ensure subdirectories exist
	dirs := []string{
		config.JobsPath(),
		config.SessionsPath(),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, DefaultDirMode); err != nil {
			return nil, fmt.Errorf("create subdirectory %s: %w", dir, err)
		}
	}

	return &Store{
		config:   config,
		redactor: redactor,
	}, nil
}

// RunnerLogWriter returns a writer for the main runner log
func (s *Store) RunnerLogWriter() (*RotatingWriter, error) {
	path := s.config.LogPath("runner.ndjson")
	return NewRotatingWriter(path, s.config, s.redactor)
}

// ErrorsLogWriter returns a writer for the error log
func (s *Store) ErrorsLogWriter() (*RotatingWriter, error) {
	path := s.config.LogPath("errors.ndjson")
	return NewRotatingWriter(path, s.config, s.redactor)
}

// IndexWriter returns a writer for the index log
func (s *Store) IndexWriter() (*Writer, error) {
	path := s.config.LogPath("index.ndjson")
	return NewWriter(path, s.redactor)
}

// JobLogWriter returns a writer for a job-specific log
func (s *Store) JobLogWriter(jobID string) (*RotatingWriter, error) {
	path := s.config.JobLogPath(jobID)
	return NewRotatingWriter(path, s.config, s.redactor)
}

// JobStdoutWriter returns a writer for job stdout capture
func (s *Store) JobStdoutWriter(jobID string) (*BoundedWriter, error) {
	path := s.config.JobStdoutPath(jobID)
	w, err := NewWriter(path, s.redactor)
	if err != nil {
		return nil, err
	}
	return NewBoundedWriter(w, s.config.RawCaptureBytes()), nil
}

// JobStderrWriter returns a writer for job stderr capture
func (s *Store) JobStderrWriter(jobID string) (*BoundedWriter, error) {
	path := s.config.JobStderrPath(jobID)
	w, err := NewWriter(path, s.redactor)
	if err != nil {
		return nil, err
	}
	return NewBoundedWriter(w, s.config.RawCaptureBytes()), nil
}

// SessionLogWriter returns a writer for a session-specific log
func (s *Store) SessionLogWriter(sessionID, turnID string) (*RotatingWriter, error) {
	path := s.config.SessionLogPath(sessionID, turnID)
	if err := os.MkdirAll(filepath.Dir(path), DefaultDirMode); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	return NewRotatingWriter(path, s.config, s.redactor)
}

// Cleanup removes expired log files
func (s *Store) Cleanup() error {
	cutoff := time.Now().Add(-s.config.RetentionDuration())

	// Clean up job logs
	if err := s.cleanupDirectory(s.config.JobsPath(), cutoff); err != nil {
		return fmt.Errorf("cleanup job logs: %w", err)
	}

	// Clean up session logs
	if err := s.cleanupDirectory(s.config.SessionsPath(), cutoff); err != nil {
		return fmt.Errorf("cleanup session logs: %w", err)
	}

	// Clean up rotated runner logs
	if err := s.cleanupRotatedLogs("runner.ndjson", cutoff); err != nil {
		return fmt.Errorf("cleanup rotated runner logs: %w", err)
	}

	// Clean up rotated error logs
	if err := s.cleanupRotatedLogs("errors.ndjson", cutoff); err != nil {
		return fmt.Errorf("cleanup rotated error logs: %w", err)
	}

	return nil
}

// cleanupDirectory removes files older than the cutoff time
func (s *Store) cleanupDirectory(dir string, cutoff time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			os.RemoveAll(path)
		}
	}

	return nil
}

// cleanupRotatedLogs removes rotated log files older than cutoff
func (s *Store) cleanupRotatedLogs(baseName string, cutoff time.Time) error {
	basePath := s.config.LogPath(baseName)

	for i := 1; i <= s.config.MaxFiles; i++ {
		path := basePath + "." + fmt.Sprint(i)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			os.Remove(path)
		}
	}

	return nil
}

// Config returns the store configuration
func (s *Store) Config() Config {
	return s.config
}
