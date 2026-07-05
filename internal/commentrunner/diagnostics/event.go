package diagnostics

import (
	"encoding/json"
	"time"
)

// SchemaVersion is the current event schema version
const SchemaVersion = 1

// Event represents a structured log event
type Event struct {
	SchemaVersion int       `json:"schema_version"`
	Timestamp     time.Time `json:"timestamp"`
	Level         Level     `json:"level"`
	Component     string    `json:"component"`
	Event         string    `json:"event"`
	Message       string    `json:"message"`
	RunnerScope   Scope     `json:"runner_scope"`
	ProcessID     int       `json:"process_id"`
	Correlation   Correlation `json:"correlation"`
	RedactionStatus string  `json:"redaction_status"`
	Details       map[string]interface{} `json:"details,omitempty"`
}

// Level represents the severity level of an event
type Level string

const (
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Scope represents the runner scope information
type Scope struct {
	Host       string `json:"host"`
	Repo       string `json:"repo,omitempty"`
	RunnerLogin string `json:"runner_login,omitempty"`
}

// Correlation holds correlation identifiers for event linking
type Correlation struct {
	CycleID            string `json:"cycle_id,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	PublicSessionID    string `json:"public_session_id,omitempty"`
	TriggerCommentID   int64  `json:"trigger_comment_id,omitempty"`
	StatusCommentID    int64  `json:"status_comment_id,omitempty"`
	AcpxRecordID       string `json:"acpx_record_id,omitempty"`
	AcpxLastTurnID     string `json:"acpx_last_turn_id,omitempty"`
	TurnCorrelationID  string `json:"turn_correlation_id,omitempty"`
}

// NewEvent creates a new event with the given parameters
func NewEvent(level Level, component, event, message string) Event {
	return Event{
		SchemaVersion:   SchemaVersion,
		Timestamp:       time.Now().UTC(),
		Level:           level,
		Component:       component,
		Event:           event,
		Message:         message,
		RedactionStatus: "clean",
		Details:         make(map[string]interface{}),
	}
}

// WithScope adds runner scope information to the event
func (e Event) WithScope(host, repo, runnerLogin string) Event {
	e.RunnerScope = Scope{
		Host:       host,
		Repo:       repo,
		RunnerLogin: runnerLogin,
	}
	return e
}

// WithProcessID adds the process ID to the event
func (e Event) WithProcessID(pid int) Event {
	e.ProcessID = pid
	return e
}

// WithCorrelation adds correlation identifiers to the event
func (e Event) WithCorrelation(c Correlation) Event {
	e.Correlation = c
	return e
}

// WithDetail adds a key-value pair to the event details
func (e Event) WithDetail(key string, value interface{}) Event {
	if e.Details == nil {
		e.Details = make(map[string]interface{})
	}
	e.Details[key] = value
	return e
}

// WithRedaction marks the event as containing redacted content
func (e Event) WithRedaction() Event {
	e.RedactionStatus = "redacted"
	return e
}

// MarshalJSON implements json.Marshaler for Event
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	return json.Marshal(alias(e))
}

// IndexEntry represents an entry in the index file
type IndexEntry struct {
	Type      string    `json:"type"` // job, session, comment, acpx_record
	ID        string    `json:"id"`
	FilePath  string    `json:"file_path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
