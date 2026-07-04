package diagnostics

import (
	"fmt"
	"os"
	"sync"
)

// SessionLogger handles session-specific logging
type SessionLogger struct {
	writers   map[string]*RotatingWriter
	store     *Store
	redactor  *Redactor
	scope     Scope
	correlation Correlation
	mu        sync.Mutex
	sessionID string
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
	sl.logTurn(turnID, LevelInfo, component, event, message, nil)
}

// LogTurnWithDetails logs an event with details for a specific turn
func (sl *SessionLogger) LogTurnWithDetails(turnID string, level Level, component, event, message string, details map[string]interface{}) {
	sl.logTurn(turnID, level, component, event, message, details)
}

// logTurn is the internal logging method
func (sl *SessionLogger) logTurn(turnID string, level Level, component, event, message string, details map[string]interface{}) {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	if sl.sessionID == "" {
		return
	}

	writer, ok := sl.writers[turnID]
	if !ok {
		w, err := sl.store.SessionLogWriter(sl.sessionID, turnID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create session writer: %v\n", err)
			return
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

	if err := writer.WriteEvent(e); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write session log: %v\n", err)
	}
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

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}

	return nil
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

	if len(errs) > 0 {
		return fmt.Errorf("sync errors: %v", errs)
	}

	return nil
}

// SessionID returns the session ID
func (sl *SessionLogger) SessionID() string {
	return sl.sessionID
}
