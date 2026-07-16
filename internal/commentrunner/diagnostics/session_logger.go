package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// SessionLogger handles session-specific logging
type SessionLogger struct {
	writers     map[string]*RotatingWriter
	store       *Store
	redactor    *Redactor
	scope       Scope
	correlation Correlation
	mu          sync.Mutex
	sessionID   string
}

// NewSessionLogger creates a new session-specific logger
func NewSessionLogger(store *Store, redactor *Redactor, scope Scope, correlation Correlation) (*SessionLogger, error) {
	return &SessionLogger{
		writers:     make(map[string]*RotatingWriter),
		store:       store,
		redactor:    redactor,
		scope:       scope,
		correlation: correlation,
	}, nil
}

// SetSessionID sets the session ID
func (sl *SessionLogger) SetSessionID(sessionID string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	sl.sessionID = sessionID
	sl.correlation.PublicSessionID = sessionID
}

// LogTurn logs an event for a specific turn
func (sl *SessionLogger) LogTurn(turnID, component, event, message string) {
	if err := sl.WriteTurnWithDetails(turnID, LevelInfo, component, event, message, nil); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write session log: %v\n", err)
	}
}

// LogTurnWithDetails logs an event with details for a specific turn
func (sl *SessionLogger) LogTurnWithDetails(turnID string, level Level, component, event, message string, details map[string]interface{}) {
	if err := sl.WriteTurnWithDetails(turnID, level, component, event, message, details); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write session log: %v\n", err)
	}
}

// WriteTurnWithDetails persists a session turn event and reports writer failures.
func (sl *SessionLogger) WriteTurnWithDetails(turnID string, level Level, component, event, message string, details map[string]interface{}) error {
	return sl.writeTurn(turnID, level, component, event, message, details)
}

// logTurn is the internal logging method
func (sl *SessionLogger) writeTurn(turnID string, level Level, component, event, message string, details map[string]interface{}) error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	if sl.sessionID == "" {
		return fmt.Errorf("session id not initialized")
	}

	writer, ok := sl.writers[turnID]
	if !ok {
		w, err := sl.store.SessionLogWriter(sl.sessionID, turnID)
		if err != nil {
			return fmt.Errorf("create session writer: %w", err)
		}
		sl.writers[turnID] = w
		writer = w
	}

	correlation := sl.correlation
	correlation.TurnCorrelationID = turnID

	e := NewEvent(level, component, event, message).
		WithScope(sl.scope.Host, sl.scope.Repo, sl.scope.RunnerLogin).
		WithProcessID(os.Getpid()).
		WithCorrelation(correlation)

	if details != nil {
		for key, value := range details {
			e = e.WithDetail(key, value)
		}
	}

	return writer.WriteEvent(e)
}

// Close closes all session writers
func (sl *SessionLogger) Close() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	var errs []error

	for turnID, writer := range sl.writers {
		if err := writer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close turn %s writer: %w", turnID, err))
		}
	}

	return errors.Join(errs...)
}

// Sync flushes all writers
func (sl *SessionLogger) Sync() error {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	var errs []error

	for _, writer := range sl.writers {
		if err := writer.Sync(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// SessionID returns the session ID
func (sl *SessionLogger) SessionID() string {
	return sl.sessionID
}
