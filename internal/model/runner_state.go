package model

import "time"

type RunnerState struct {
	Version int                   `json:"version"`
	Runner  RunnerMetadata        `json:"runner"`
	Polling PollingState          `json:"polling"`
	Repos   map[string]*RepoState `json:"repos,omitempty"`
}

type RunnerMetadata struct {
	InstanceID string `json:"instance_id,omitempty"`
	StatePath  string `json:"state_path,omitempty"`
}

type PollingState struct {
	NotificationsCursor string    `json:"notifications_cursor,omitempty"`
	RepoCursor          string    `json:"repo_cursor,omitempty"`
	ETag                string    `json:"etag,omitempty"`
	LastModified        string    `json:"last_modified,omitempty"`
	PollInterval        string    `json:"poll_interval,omitempty"`
	RateLimitRemaining  int       `json:"rate_limit_remaining,omitempty"`
	RateLimitReset      time.Time `json:"rate_limit_reset,omitempty"`
}

type RepoState struct {
	RepoID                string                         `json:"repo_id,omitempty"`
	Validators            ValidatorState                 `json:"validators"`
	FirstObservedComments map[string]ObservedComment     `json:"first_observed_comments,omitempty"`
	Jobs                  map[string]*JobState           `json:"jobs,omitempty"`
	PublicSessions        map[string]*PublicSessionState `json:"public_sessions,omitempty"`
	Workspaces            map[string]*WorkspaceState     `json:"workspaces,omitempty"`
	Sandboxes             map[string]*SandboxState       `json:"sandboxes,omitempty"`
	Acpx                  map[string]*AcpxState          `json:"acpx,omitempty"`
	StatusComments        map[string]*StatusCommentState `json:"status_comments,omitempty"`
	CLIProvenance         map[string]*CLIProvenanceState `json:"cli_provenance,omitempty"`
	Idempotency           map[string]IdempotencyRecord   `json:"idempotency,omitempty"`
	Cancellation          map[string]CancellationState   `json:"cancellation,omitempty"`
}

type ValidatorState struct {
	NotificationsETag string    `json:"notifications_etag,omitempty"`
	IssueETag         string    `json:"issue_etag,omitempty"`
	CommentsETag      string    `json:"comments_etag,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
}

type ObservedComment struct {
	CommentID  int64     `json:"comment_id"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type JobState struct {
	ID              string    `json:"id,omitempty"`
	Command         string    `json:"command,omitempty"`
	Status          string    `json:"status,omitempty"`
	PublicSessionID string    `json:"public_session_id,omitempty"`
	WorkspaceID     string    `json:"workspace_id,omitempty"`
	SandboxID       string    `json:"sandbox_id,omitempty"`
	AcpxID          string    `json:"acpx_id,omitempty"`
	StatusCommentID int64     `json:"status_comment_id,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type PublicSessionState struct {
	RepoID          string    `json:"repo_id,omitempty"`
	PublicSessionID string    `json:"public_session_id,omitempty"`
	JobID           string    `json:"job_id,omitempty"`
	ProviderID      string    `json:"provider_id,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type WorkspaceState struct {
	ID        string    `json:"id,omitempty"`
	Path      string    `json:"path,omitempty"`
	Repo      string    `json:"repo,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Locked    bool      `json:"locked,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type SandboxState struct {
	ID        string            `json:"id,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitempty"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}

type AcpxState struct {
	ID          string            `json:"id,omitempty"`
	SessionID   string            `json:"session_id,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CancelledAt time.Time         `json:"cancelled_at,omitempty"`
}

type StatusCommentState struct {
	IssueID   string    `json:"issue_id,omitempty"`
	CommentID int64     `json:"comment_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type CLIProvenanceState struct {
	CommandKey string    `json:"command_key,omitempty"`
	Source     string    `json:"source,omitempty"`
	CommentID  int64     `json:"comment_id,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type IdempotencyRecord struct {
	Key        string    `json:"key,omitempty"`
	Kind       string    `json:"kind,omitempty"`
	ResourceID string    `json:"resource_id,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
}

type CancellationState struct {
	Key         string    `json:"key,omitempty"`
	JobID       string    `json:"job_id,omitempty"`
	CancelledAt time.Time `json:"cancelled_at,omitempty"`
}
