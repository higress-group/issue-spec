package diagnostics

import (
	"fmt"
	"os"
	"sync"
)

// JobLogger handles job-specific logging
type JobLogger struct {
	writer    *RotatingWriter
	stdout    *BoundedWriter
	stderr    *BoundedWriter
	store     *Store
	redactor  *Redactor
	scope     Scope
	correlation Correlation
	mu        sync.Mutex
	jobID     string
}

// NewJobLogger creates a new job-specific logger
func NewJobLogger(store *Store, redactor *Redactor, scope Scope, correlation Correlation) (*JobLogger, error) {
	return &JobLogger{
		store:       store,
		redactor:    redactor,
		scope:       scope,
		correlation: correlation,
	}, nil
}

// Initialize sets up the job log writers
func (jl *JobLogger) Initialize(jobID string) error {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	jl.jobID = jobID

	// Create job log writer
	writer, err := jl.store.JobLogWriter(jobID)
	if err != nil {
		return fmt.Errorf("create job log writer: %w", err)
	}
	jl.writer = writer

	// Create stdout writer
	stdout, err := jl.store.JobStdoutWriter(jobID)
	if err != nil {
		return fmt.Errorf("create stdout writer: %w", err)
	}
	jl.stdout = stdout

	// Create stderr writer
	stderr, err := jl.store.JobStderrWriter(jobID)
	if err != nil {
		return fmt.Errorf("create stderr writer: %w", err)
	}
	jl.stderr = stderr

	// Update correlation with job ID
	jl.correlation.JobID = jobID

	return nil
}

// LogEvent logs a job event
func (jl *JobLogger) LogEvent(level Level, component, event, message string) {
	jl.logEvent(level, component, event, message, nil)
}

// LogEventWithDetails logs a job event with details
func (jl *JobLogger) LogEventWithDetails(level Level, component, event, message string, details map[string]interface{}) {
	jl.logEvent(level, component, event, message, details)
}

// logEvent is the internal logging method
func (jl *JobLogger) logEvent(level Level, component, event, message string, details map[string]interface{}) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if jl.writer == nil {
		return
	}

	e := NewEvent(level, component, event, message).
		WithScope(jl.scope.Host, jl.scope.Repo, jl.scope.RunnerLogin).
		WithProcessID(os.Getpid()).
		WithCorrelation(jl.correlation)

	if details != nil {
		for key, value := range details {
			e = e.WithDetail(key, value)
		}
	}

	if err := jl.writer.WriteEvent(e); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write job log: %v\n", err)
	}
}

// WriteStdout writes to the job stdout file
func (jl *JobLogger) WriteStdout(data []byte) (int, error) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if jl.stdout == nil {
		return 0, fmt.Errorf("stdout writer not initialized")
	}

	return jl.stdout.WriteBytes(data)
}

// WriteStderr writes to the job stderr file
func (jl *JobLogger) WriteStderr(data []byte) (int, error) {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	if jl.stderr == nil {
		return 0, fmt.Errorf("stderr writer not initialized")
	}

	return jl.stderr.WriteBytes(data)
}

// Close closes the job log writers
func (jl *JobLogger) Close() error {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	var errs []error

	if jl.writer != nil {
		if err := jl.writer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close job writer: %w", err))
		}
	}

	if jl.stdout != nil {
		if err := jl.stdout.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close stdout writer: %w", err))
		}
	}

	if jl.stderr != nil {
		if err := jl.stderr.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close stderr writer: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}

	return nil
}

// Sync flushes all writers
func (jl *JobLogger) Sync() error {
	jl.mu.Lock()
	defer jl.mu.Unlock()

	var errs []error

	if jl.writer != nil {
		if err := jl.writer.Sync(); err != nil {
			errs = append(errs, err)
		}
	}

	if jl.stdout != nil {
		if err := jl.stdout.Sync(); err != nil {
			errs = append(errs, err)
		}
	}

	if jl.stderr != nil {
		if err := jl.stderr.Sync(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sync errors: %v", errs)
	}

	return nil
}

// JobID returns the job ID
func (jl *JobLogger) JobID() string {
	return jl.jobID
}

// StdoutPath returns the path to the stdout file
func (jl *JobLogger) StdoutPath() string {
	if jl.stdout != nil {
		return jl.stdout.Path()
	}
	return ""
}

// StderrPath returns the path to the stderr file
func (jl *JobLogger) StderrPath() string {
	if jl.stderr != nil {
		return jl.stderr.Path()
	}
	return ""
}

// IsStdoutTruncated returns whether stdout was truncated
func (jl *JobLogger) IsStdoutTruncated() bool {
	if jl.stdout != nil {
		return jl.stdout.IsTruncated()
	}
	return false
}

// IsStderrTruncated returns whether stderr was truncated
func (jl *JobLogger) IsStderrTruncated() bool {
	if jl.stderr != nil {
		return jl.stderr.IsTruncated()
	}
	return false
}
