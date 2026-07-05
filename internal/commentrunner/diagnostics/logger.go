package diagnostics

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// Logger provides structured logging capabilities
type Logger struct {
	store         *Store
	runnerWriter  *RotatingWriter
	errorWriter   *RotatingWriter
	indexWriter   *Writer
	config        Config
	redactor      *Redactor
	correlation   Correlation
	scope         Scope
	mu            sync.Mutex
	processID     int
}

// NewLogger creates a new logger with the given configuration
func NewLogger(config Config) (*Logger, error) {
	redactor := NewRedactor()
	store, err := NewStore(config, redactor)
	if err != nil {
		return nil, fmt.Errorf("create store: %w", err)
	}

	runnerWriter, err := store.RunnerLogWriter()
	if err != nil {
		return nil, fmt.Errorf("create runner writer: %w", err)
	}

	errorWriter, err := store.ErrorsLogWriter()
	if err != nil {
		return nil, fmt.Errorf("create error writer: %w", err)
	}

	indexWriter, err := store.IndexWriter()
	if err != nil {
		return nil, fmt.Errorf("create index writer: %w", err)
	}

	return &Logger{
		store:        store,
		runnerWriter: runnerWriter,
		errorWriter:  errorWriter,
		indexWriter:  indexWriter,
		config:       config,
		redactor:     redactor,
		processID:    os.Getpid(),
	}, nil
}

// WithScope sets the runner scope for the logger
func (l *Logger) WithScope(host, repo, runnerLogin string) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.scope = Scope{
		Host:        host,
		Repo:        repo,
		RunnerLogin: runnerLogin,
	}

	return l
}

// WithCorrelation sets the correlation context for the logger
func (l *Logger) WithCorrelation(c Correlation) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.correlation = c
	return l
}

// WithJobID adds a job ID to the correlation context
func (l *Logger) WithJobID(jobID string) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.correlation.JobID = jobID
	return l
}

// WithCycleID adds a cycle ID to the correlation context
func (l *Logger) WithCycleID(cycleID string) *Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.correlation.CycleID = cycleID
	return l
}

// LogEvent logs an event at the specified level
func (l *Logger) LogEvent(level Level, component, event, message string) {
	l.logEvent(level, component, event, message, nil)
}

// LogEventWithDetails logs an event with additional details
func (l *Logger) LogEventWithDetails(level Level, component, event, message string, details map[string]interface{}) {
	l.logEvent(level, component, event, message, details)
}

// Info logs an info event
func (l *Logger) Info(component, event, message string) {
	l.LogEvent(LevelInfo, component, event, message)
}

// Warn logs a warning event
func (l *Logger) Warn(component, event, message string) {
	l.LogEvent(LevelWarn, component, event, message)
}

// Error logs an error event
func (l *Logger) Error(component, event, message string) {
	l.LogEvent(LevelError, component, event, message)
}

// logEvent is the internal logging method
func (l *Logger) logEvent(level Level, component, event, message string, details map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e := NewEvent(level, component, event, message).
		WithScope(l.scope.Host, l.scope.Repo, l.scope.RunnerLogin).
		WithProcessID(l.processID).
		WithCorrelation(l.correlation)

	if details != nil {
		for key, value := range details {
			e = e.WithDetail(key, value)
		}
	}

	// Write to runner log
	if l.runnerWriter != nil {
		if err := l.runnerWriter.WriteEvent(e); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write runner log: %v\n", err)
		}
	}

	// Write to error log for warnings and errors
	if level == LevelWarn || level == LevelError {
		if l.errorWriter != nil {
			if err := l.errorWriter.WriteEvent(e); err != nil {
				fmt.Fprintf(os.Stderr, "failed to write error log: %v\n", err)
			}
		}
	}
}

// LogIndex writes an index entry
func (l *Logger) LogIndex(entry IndexEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal index entry: %w", err)
	}

	redacted, err := l.redactor.RedactJSON(data)
	if err != nil {
		// If redaction fails, use the original data but mark it
		redacted = data
	}

	if _, err := l.indexWriter.WriteBytes(redacted); err != nil {
		return fmt.Errorf("write index entry: %w", err)
	}

	// Add newline for NDJSON
	l.indexWriter.WriteBytes([]byte("\n"))

	return nil
}

// JobLogger returns a job-specific logger
func (l *Logger) JobLogger(jobID string) (*JobLogger, error) {
	return NewJobLogger(l.store, l.redactor, l.scope, l.correlation)
}

// SessionLogger returns a session-specific logger
func (l *Logger) SessionLogger(sessionID string) (*SessionLogger, error) {
	return NewSessionLogger(l.store, l.redactor, l.scope, l.correlation)
}

// Sync flushes all log writers
func (l *Logger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error

	if l.runnerWriter != nil {
		if err := l.runnerWriter.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync runner writer: %w", err))
		}
	}

	if l.errorWriter != nil {
		if err := l.errorWriter.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync error writer: %w", err))
		}
	}

	if l.indexWriter != nil {
		if err := l.indexWriter.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync index writer: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("sync errors: %v", errs)
	}

	return nil
}

// Close closes all log writers
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var errs []error

	if l.runnerWriter != nil {
		if err := l.runnerWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close runner writer: %w", err))
		}
	}

	if l.errorWriter != nil {
		if err := l.errorWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close error writer: %w", err))
		}
	}

	if l.indexWriter != nil {
		if err := l.indexWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close index writer: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}

	return nil
}

// Cleanup performs log cleanup
func (l *Logger) Cleanup() error {
	return l.store.Cleanup()
}

// Config returns the logger configuration
func (l *Logger) Config() Config {
	return l.config
}

// Correlation returns the current correlation context
func (l *Logger) Correlation() Correlation {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.correlation
}
