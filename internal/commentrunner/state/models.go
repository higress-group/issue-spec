package state

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 1

type LifecycleStatus string

const (
	StatusQueued      LifecycleStatus = "queued"
	StatusDispatched  LifecycleStatus = "dispatched"
	StatusRunning     LifecycleStatus = "running"
	StatusCompleted   LifecycleStatus = "completed"
	StatusFailed      LifecycleStatus = "failed"
	StatusRejected    LifecycleStatus = "rejected"
	StatusCancelled   LifecycleStatus = "cancelled"
	StatusInterrupted LifecycleStatus = "interrupted"
)

var ErrInvalidTransition = errors.New("invalid job lifecycle transition")

type RunnerState struct {
	SchemaVersion    int                          `json:"schema_version"`
	CreatedAt        time.Time                    `json:"created_at,omitempty"`
	UpdatedAt        time.Time                    `json:"updated_at,omitempty"`
	Repositories     map[string]RepositoryState   `json:"repositories,omitempty"`
	Jobs             map[string]Job               `json:"jobs,omitempty"`
	PublicSessions   map[string]PublicSession     `json:"public_sessions,omitempty"`
	Workspaces       map[string]WorkspaceMetadata `json:"workspaces,omitempty"`
	Cancellations    map[string]Cancellation      `json:"cancellations,omitempty"`
	StatusWritebacks map[string]StatusWriteback   `json:"status_writebacks,omitempty"`
	Idempotency      IdempotencyIndex             `json:"idempotency,omitempty"`
}

type IdempotencyIndex struct {
	CommandJobs      map[string]string `json:"command_jobs,omitempty"`
	CancelRequests   map[string]string `json:"cancel_requests,omitempty"`
	StatusWritebacks map[string]string `json:"status_writebacks,omitempty"`
}

type RepositoryState struct {
	Host                      string                 `json:"host,omitempty"`
	Repo                      string                 `json:"repo,omitempty"`
	Backend                   string                 `json:"backend,omitempty"`
	RunnerLogin               string                 `json:"runner_login,omitempty"`
	SubscriptionPreflight     PreflightRecord        `json:"subscription_preflight,omitempty"`
	NotificationCursor        CursorState            `json:"notification_cursor,omitempty"`
	RepositoryCommentCursor   CursorState            `json:"repository_comment_cursor,omitempty"`
	IssueCommentCursors       map[string]CursorState `json:"issue_comment_cursors,omitempty"`
	NotificationThreadCursors map[string]CursorState `json:"notification_thread_cursors,omitempty"`
	FallbackCadence           FallbackCadence        `json:"fallback_cadence,omitempty"`
	RateLimit                 RateLimitState         `json:"rate_limit,omitempty"`
	LastSeenCommentID         int64                  `json:"last_seen_comment_id,omitempty"`
	LastSeenCommentUpdatedAt  time.Time              `json:"last_seen_comment_updated_at,omitempty"`
}

type CursorState struct {
	Resource             string         `json:"resource,omitempty"`
	Cursor               string         `json:"cursor,omitempty"`
	LastURL              string         `json:"last_url,omitempty"`
	LastSeenID           int64          `json:"last_seen_id,omitempty"`
	LastSeenAt           time.Time      `json:"last_seen_at,omitempty"`
	ETag                 string         `json:"etag,omitempty"`
	LastModified         string         `json:"last_modified,omitempty"`
	XPollIntervalSeconds int            `json:"x_poll_interval_seconds,omitempty"`
	LastPollAt           time.Time      `json:"last_poll_at,omitempty"`
	LastSuccessfulPollAt time.Time      `json:"last_successful_poll_at,omitempty"`
	LastStatusCode       int            `json:"last_status_code,omitempty"`
	RateLimit            RateLimitState `json:"rate_limit,omitempty"`
}

type FallbackCadence struct {
	Enabled         bool      `json:"enabled,omitempty"`
	IntervalSeconds int       `json:"interval_seconds,omitempty"`
	NextPollAt      time.Time `json:"next_poll_at,omitempty"`
	LastFallbackAt  time.Time `json:"last_fallback_at,omitempty"`
}

type RateLimitState struct {
	Limit             int       `json:"limit,omitempty"`
	Remaining         int       `json:"remaining,omitempty"`
	ResetAt           time.Time `json:"reset_at,omitempty"`
	Resource          string    `json:"resource,omitempty"`
	RetryAfterSeconds int       `json:"retry_after_seconds,omitempty"`
}

type PreflightRecord struct {
	CheckedAt   time.Time `json:"checked_at,omitempty"`
	Result      string    `json:"result,omitempty"`
	Override    bool      `json:"override,omitempty"`
	Remediation string    `json:"remediation,omitempty"`
}

type SeenComment struct {
	Host                          string    `json:"host,omitempty"`
	Repo                          string    `json:"repo,omitempty"`
	IssueNumber                   int       `json:"issue_number,omitempty"`
	CommentID                     int64     `json:"comment_id,omitempty"`
	HTMLURL                       string    `json:"html_url,omitempty"`
	APIURL                        string    `json:"api_url,omitempty"`
	AuthorLogin                   string    `json:"author_login,omitempty"`
	FirstObservedAt               time.Time `json:"first_observed_at,omitempty"`
	FirstObservedUpdatedAt        time.Time `json:"first_observed_updated_at,omitempty"`
	FirstObservedBodyHash         string    `json:"first_observed_body_hash,omitempty"`
	FirstObservedRevision         string    `json:"first_observed_revision,omitempty"`
	ProducedCommandCandidate      bool      `json:"produced_command_candidate,omitempty"`
	CommandCandidateID            string    `json:"command_candidate_id,omitempty"`
	CommandName                   string    `json:"command_name,omitempty"`
	CommandIdempotencyKey         string    `json:"command_idempotency_key,omitempty"`
	CancelIdempotencyKey          string    `json:"cancel_idempotency_key,omitempty"`
	StatusWritebackIdempotencyKey string    `json:"status_writeback_idempotency_key,omitempty"`
}

type Job struct {
	ID                    string                  `json:"id"`
	Repo                  string                  `json:"repo,omitempty"`
	IssueNumber           int                     `json:"issue_number,omitempty"`
	PublicSessionID       string                  `json:"public_session_id,omitempty"`
	AcpxRecordID          string                  `json:"acpx_record_id,omitempty"`
	CoordinatorKind       string                  `json:"coordinator_kind,omitempty"`
	Model                 string                  `json:"model,omitempty"`
	SessionCreatorLogin   string                  `json:"session_creator_login,omitempty"`
	TriggeringUserLogin   string                  `json:"triggering_user_login,omitempty"`
	TriggerCommentID      int64                   `json:"trigger_comment_id,omitempty"`
	StatusCommentID       int64                   `json:"status_comment_id,omitempty"`
	StatusCommentURL      string                  `json:"status_comment_url,omitempty"`
	CommandID             string                  `json:"command_id,omitempty"`
	CommandName           string                  `json:"command_name,omitempty"`
	CommandPrompt         string                  `json:"command_prompt,omitempty"`
	CommandIdempotencyKey string                  `json:"command_idempotency_key,omitempty"`
	StatusWritebackKey    string                  `json:"status_writeback_key,omitempty"`
	Status                LifecycleStatus         `json:"status,omitempty"`
	CreatedAt             time.Time               `json:"created_at,omitempty"`
	UpdatedAt             time.Time               `json:"updated_at,omitempty"`
	DispatchedAt          time.Time               `json:"dispatched_at,omitempty"`
	StartedAt             time.Time               `json:"started_at,omitempty"`
	FinishedAt            time.Time               `json:"finished_at,omitempty"`
	FirstObservedComment  SeenComment             `json:"first_observed_comment,omitempty"`
	SourceLabels          []string                `json:"source_labels,omitempty"`
	ContextBundle         ContextBundleProvenance `json:"context_bundle,omitempty"`
	DispatchIntent        DispatchIntent          `json:"dispatch_intent,omitempty"`
	Workspace             WorkspaceMetadata       `json:"workspace,omitempty"`
	Sandbox               SandboxMetadata         `json:"sandbox,omitempty"`
	Acpx                  AcpxMetadata            `json:"acpx,omitempty"`
	CLIDirect             []CLIDirectProvenance   `json:"cli_direct,omitempty"`
	Restart               RestartMetadata         `json:"restart,omitempty"`
	CoordinatorSummary    string                  `json:"coordinator_summary,omitempty"`
	Diagnostics           []string                `json:"diagnostics,omitempty"`
}

type PublicSession struct {
	Repo            string            `json:"repo"`
	PublicSessionID string            `json:"public_session_id"`
	IssueNumber     int               `json:"issue_number,omitempty"`
	AcpxRecordID    string            `json:"acpx_record_id"`
	CreatorLogin    string            `json:"creator_login,omitempty"`
	Status          LifecycleStatus   `json:"status,omitempty"`
	Acpx            AcpxMetadata      `json:"acpx,omitempty"`
	Workspace       WorkspaceMetadata `json:"workspace,omitempty"`
	Queue           SessionQueue      `json:"queue,omitempty"`
	Lock            SessionLock       `json:"lock,omitempty"`
	CreatedAt       time.Time         `json:"created_at,omitempty"`
	LastUsedAt      time.Time         `json:"last_used_at,omitempty"`
	LastJobID       string            `json:"last_job_id,omitempty"`
}

type WorkspaceMetadata struct {
	ID              string    `json:"id,omitempty"`
	Path            string    `json:"path,omitempty"`
	Repo            string    `json:"repo,omitempty"`
	CloneURL        string    `json:"clone_url,omitempty"`
	Branch          string    `json:"branch,omitempty"`
	Ref             string    `json:"ref,omitempty"`
	CheckoutSHA     string    `json:"checkout_sha,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	LastUsedAt      time.Time `json:"last_used_at,omitempty"`
	RetentionPolicy string    `json:"retention_policy,omitempty"`
	CleanupAfter    time.Time `json:"cleanup_after,omitempty"`
	Dirty           bool      `json:"dirty,omitempty"`
	Uncertain       bool      `json:"uncertain,omitempty"`
}

type SessionQueue struct {
	PendingJobIDs    []string `json:"pending_job_ids,omitempty"`
	AcceptedSequence int64    `json:"accepted_sequence,omitempty"`
}

type SessionLock struct {
	OwnerJobID         string    `json:"owner_job_id,omitempty"`
	AcquiredAt         time.Time `json:"acquired_at,omitempty"`
	WorkspaceLockToken string    `json:"workspace_lock_token,omitempty"`
	WorkspaceLockPath  string    `json:"workspace_lock_path,omitempty"`
	StaleRecoveredAt   time.Time `json:"stale_recovered_at,omitempty"`
}

type Cancellation struct {
	ID                    string          `json:"id"`
	IdempotencyKey        string          `json:"idempotency_key"`
	Repo                  string          `json:"repo,omitempty"`
	TriggerCommentID      int64           `json:"trigger_comment_id,omitempty"`
	CancelingUserLogin    string          `json:"canceling_user_login,omitempty"`
	TargetPublicSessionID string          `json:"target_public_session_id,omitempty"`
	TargetJobID           string          `json:"target_job_id,omitempty"`
	AcpxResult            string          `json:"acpx_result,omitempty"`
	Status                LifecycleStatus `json:"status,omitempty"`
	CreatedAt             time.Time       `json:"created_at,omitempty"`
	CancelledAt           time.Time       `json:"cancelled_at,omitempty"`
	DirtyWorkspace        bool            `json:"dirty_workspace,omitempty"`
	WorkspaceUncertain    bool            `json:"workspace_uncertain,omitempty"`
	Diagnostics           []string        `json:"diagnostics,omitempty"`
}

type StatusWriteback struct {
	IdempotencyKey   string          `json:"idempotency_key"`
	JobID            string          `json:"job_id,omitempty"`
	Repo             string          `json:"repo,omitempty"`
	IssueNumber      int             `json:"issue_number,omitempty"`
	TriggerCommentID int64           `json:"trigger_comment_id,omitempty"`
	CommentID        int64           `json:"comment_id,omitempty"`
	URL              string          `json:"url,omitempty"`
	Status           LifecycleStatus `json:"status,omitempty"`
	LastAttemptAt    time.Time       `json:"last_attempt_at,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at,omitempty"`
	LastError        string          `json:"last_error,omitempty"`
}

type DispatchIntent struct {
	CommandIdempotencyKey string    `json:"command_idempotency_key,omitempty"`
	RunnerJobID           string    `json:"runner_job_id,omitempty"`
	PublicSessionID       string    `json:"public_session_id,omitempty"`
	AcpxRecordID          string    `json:"acpx_record_id,omitempty"`
	TurnSequence          int64     `json:"turn_sequence,omitempty"`
	TurnCorrelationToken  string    `json:"turn_correlation_token,omitempty"`
	ContextBundleHash     string    `json:"context_bundle_hash,omitempty"`
	StatusCommentID       int64     `json:"status_comment_id,omitempty"`
	WorkspaceLockOwner    string    `json:"workspace_lock_owner,omitempty"`
	PersistedAt           time.Time `json:"persisted_at,omitempty"`
}

type ContextBundleProvenance struct {
	SchemaVersion      int           `json:"schema_version,omitempty"`
	Hash               string        `json:"hash,omitempty"`
	CommandCandidateID string        `json:"command_candidate_id,omitempty"`
	SelectedArtifacts  []ArtifactRef `json:"selected_artifacts,omitempty"`
	PromptBytes        int           `json:"prompt_bytes,omitempty"`
	Truncated          bool          `json:"truncated,omitempty"`
	Sanitized          bool          `json:"sanitized,omitempty"`
}

type ArtifactRef struct {
	ID          string `json:"id,omitempty"`
	URL         string `json:"url,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
	Kind        string `json:"kind,omitempty"`
}

type SandboxMetadata struct {
	Enabled          bool              `json:"enabled,omitempty"`
	UnsafeNoSandbox  bool              `json:"unsafe_no_sandbox,omitempty"`
	SandboxProvider  string            `json:"sandbox_provider,omitempty"`
	FSBoundary       string            `json:"fs_boundary,omitempty"`
	PreflightResult  string            `json:"preflight_result,omitempty"`
	Bwrap            map[string]string `json:"bwrap,omitempty"`
	EnvDecisions     []string          `json:"env_decisions,omitempty"`
	TempPaths        map[string]string `json:"temp_paths,omitempty"`
	MountPlanSummary string            `json:"mount_plan_summary,omitempty"`
	Diagnostics      string            `json:"diagnostics,omitempty"`
	CheckedAt        time.Time         `json:"checked_at,omitempty"`
}

type AcpxMetadata struct {
	StableRecordID    string            `json:"stable_record_id,omitempty"`
	TrueSessionID     string            `json:"true_session_id,omitempty"`
	ProviderSessionID string            `json:"provider_session_id,omitempty"`
	LastTurnID        string            `json:"last_turn_id,omitempty"`
	RefreshedAt       time.Time         `json:"refreshed_at,omitempty"`
	Raw               map[string]string `json:"raw,omitempty"`
}

type CLIDirectProvenance struct {
	CommandName   string        `json:"command_name,omitempty"`
	RedactedArgs  []string      `json:"redacted_args,omitempty"`
	Backend       string        `json:"backend,omitempty"`
	ExitCode      int           `json:"exit_code,omitempty"`
	StdoutSummary string        `json:"stdout_summary,omitempty"`
	StderrSummary string        `json:"stderr_summary,omitempty"`
	ArtifactRefs  []ArtifactRef `json:"artifact_refs,omitempty"`
	Diagnostics   string        `json:"diagnostics,omitempty"`
}

type RestartMetadata struct {
	ReconciledAt         time.Time       `json:"reconciled_at,omitempty"`
	RecoveredStatus      LifecycleStatus `json:"recovered_status,omitempty"`
	Ambiguous            bool            `json:"ambiguous,omitempty"`
	WorkspaceMarkedDirty bool            `json:"workspace_marked_dirty,omitempty"`
	Diagnostics          string          `json:"diagnostics,omitempty"`
}

func NewState() RunnerState {
	st := RunnerState{SchemaVersion: SchemaVersion}
	st.Normalize()
	return st
}

func (s *RunnerState) Normalize() {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	if s.Repositories == nil {
		s.Repositories = map[string]RepositoryState{}
	}
	if s.Jobs == nil {
		s.Jobs = map[string]Job{}
	}
	if s.PublicSessions == nil {
		s.PublicSessions = map[string]PublicSession{}
	}
	if s.Workspaces == nil {
		s.Workspaces = map[string]WorkspaceMetadata{}
	}
	if s.Cancellations == nil {
		s.Cancellations = map[string]Cancellation{}
	}
	if s.StatusWritebacks == nil {
		s.StatusWritebacks = map[string]StatusWriteback{}
	}
	if s.Idempotency.CommandJobs == nil {
		s.Idempotency.CommandJobs = map[string]string{}
	}
	if s.Idempotency.CancelRequests == nil {
		s.Idempotency.CancelRequests = map[string]string{}
	}
	if s.Idempotency.StatusWritebacks == nil {
		s.Idempotency.StatusWritebacks = map[string]string{}
	}
	for key, repo := range s.Repositories {
		if repo.IssueCommentCursors == nil {
			repo.IssueCommentCursors = map[string]CursorState{}
		}
		if repo.NotificationThreadCursors == nil {
			repo.NotificationThreadCursors = map[string]CursorState{}
		}
		s.Repositories[key] = repo
	}
	for id, job := range s.Jobs {
		if job.ID == "" {
			job.ID = id
		}
		if job.Status == "" {
			job.Status = StatusQueued
		}
		if job.CommandIdempotencyKey != "" {
			s.Idempotency.CommandJobs[job.CommandIdempotencyKey] = job.ID
		}
		if job.StatusWritebackKey != "" {
			s.Idempotency.StatusWritebacks[job.StatusWritebackKey] = job.StatusWritebackKey
		}
		s.Jobs[id] = job
	}
	for id, cancel := range s.Cancellations {
		if cancel.ID == "" {
			cancel.ID = id
		}
		if cancel.IdempotencyKey != "" {
			s.Idempotency.CancelRequests[cancel.IdempotencyKey] = cancel.ID
		}
		s.Cancellations[id] = cancel
	}
	for key, writeback := range s.StatusWritebacks {
		if writeback.IdempotencyKey == "" {
			writeback.IdempotencyKey = key
		}
		if key != writeback.IdempotencyKey {
			delete(s.StatusWritebacks, key)
		}
		s.Idempotency.StatusWritebacks[writeback.IdempotencyKey] = writeback.IdempotencyKey
		s.StatusWritebacks[writeback.IdempotencyKey] = writeback
	}
}

func PublicSessionKey(repo, publicSessionID string) string {
	return strings.TrimSpace(repo) + "#" + strings.TrimSpace(publicSessionID)
}

func (s *RunnerState) CreateCommandJob(job Job) (Job, bool, error) {
	s.Normalize()
	if strings.TrimSpace(job.CommandIdempotencyKey) == "" {
		return Job{}, false, fmt.Errorf("command job requires idempotency key")
	}
	if existingID := s.Idempotency.CommandJobs[job.CommandIdempotencyKey]; existingID != "" {
		existing, ok := s.Jobs[existingID]
		if !ok {
			return Job{}, false, fmt.Errorf("idempotency index points to missing job %q", existingID)
		}
		return existing, false, nil
	}
	if err := s.UpsertJob(job); err != nil {
		return Job{}, false, err
	}
	return s.Jobs[job.ID], true, nil
}

func (s *RunnerState) UpsertJob(job Job) error {
	s.Normalize()
	if strings.TrimSpace(job.ID) == "" {
		return fmt.Errorf("job id is required")
	}
	if job.Status == "" {
		job.Status = StatusQueued
	}
	if !job.Status.Valid() {
		return fmt.Errorf("invalid job status %q", job.Status)
	}
	s.Jobs[job.ID] = job
	if job.CommandIdempotencyKey != "" {
		s.Idempotency.CommandJobs[job.CommandIdempotencyKey] = job.ID
	}
	if job.StatusWritebackKey != "" {
		s.Idempotency.StatusWritebacks[job.StatusWritebackKey] = job.StatusWritebackKey
	}
	return nil
}

func (s *RunnerState) UpsertWorkspace(workspace WorkspaceMetadata) error {
	s.Normalize()
	if strings.TrimSpace(workspace.ID) == "" {
		return fmt.Errorf("workspace id is required")
	}
	if strings.TrimSpace(workspace.Path) == "" {
		return fmt.Errorf("workspace path is required")
	}
	if strings.TrimSpace(workspace.Repo) == "" {
		return fmt.Errorf("workspace repo is required")
	}
	s.Workspaces[workspace.ID] = workspace
	return nil
}

func (s *RunnerState) GetWorkspace(id string) (WorkspaceMetadata, bool) {
	s.Normalize()
	workspace, ok := s.Workspaces[strings.TrimSpace(id)]
	return workspace, ok
}

func (s *RunnerState) UpdateJobStatus(jobID string, next LifecycleStatus, at time.Time, diagnostics ...string) (Job, error) {
	s.Normalize()
	job, ok := s.Jobs[jobID]
	if !ok {
		return Job{}, fmt.Errorf("job %q not found", jobID)
	}
	if !next.Valid() {
		return Job{}, fmt.Errorf("invalid job status %q", next)
	}
	if !canTransition(job.Status, next) {
		return Job{}, fmt.Errorf("%w: %s to %s", ErrInvalidTransition, job.Status, next)
	}
	job.Status = next
	job.UpdatedAt = at
	switch next {
	case StatusDispatched:
		job.DispatchedAt = firstTime(job.DispatchedAt, at)
	case StatusRunning:
		job.StartedAt = firstTime(job.StartedAt, at)
	case StatusCompleted, StatusFailed, StatusRejected, StatusCancelled, StatusInterrupted:
		job.FinishedAt = firstTime(job.FinishedAt, at)
	}
	job.Diagnostics = append(job.Diagnostics, diagnostics...)
	s.Jobs[jobID] = job
	return job, nil
}

func (s *RunnerState) JobsForReconciliation() []Job {
	s.Normalize()
	var jobs []Job
	for _, job := range s.Jobs {
		if job.Status.NeedsReconciliation() {
			jobs = append(jobs, job)
		}
	}
	sortJobs(jobs)
	return jobs
}

func (s *RunnerState) ListJobs() []Job {
	s.Normalize()
	jobs := make([]Job, 0, len(s.Jobs))
	for _, job := range s.Jobs {
		jobs = append(jobs, job)
	}
	sortJobs(jobs)
	return jobs
}

func (s *RunnerState) UpsertPublicSession(session PublicSession) error {
	s.Normalize()
	if strings.TrimSpace(session.Repo) == "" || strings.TrimSpace(session.PublicSessionID) == "" {
		return fmt.Errorf("public session requires repo and public session id")
	}
	if strings.TrimSpace(session.AcpxRecordID) == "" {
		return fmt.Errorf("public session requires acpx record id")
	}
	if session.Status == "" {
		session.Status = StatusQueued
	}
	if !session.Status.Valid() {
		return fmt.Errorf("invalid session status %q", session.Status)
	}
	s.PublicSessions[PublicSessionKey(session.Repo, session.PublicSessionID)] = session
	return nil
}

func (s *RunnerState) GetPublicSession(repo, publicSessionID string) (PublicSession, bool) {
	s.Normalize()
	session, ok := s.PublicSessions[PublicSessionKey(repo, publicSessionID)]
	return session, ok
}

func (s *RunnerState) UpsertCancellation(cancel Cancellation) error {
	s.Normalize()
	if strings.TrimSpace(cancel.ID) == "" {
		return fmt.Errorf("cancellation id is required")
	}
	if strings.TrimSpace(cancel.IdempotencyKey) == "" {
		return fmt.Errorf("cancellation idempotency key is required")
	}
	if cancel.Status == "" {
		cancel.Status = StatusQueued
	}
	if !cancel.Status.Valid() {
		return fmt.Errorf("invalid cancellation status %q", cancel.Status)
	}
	s.Cancellations[cancel.ID] = cancel
	s.Idempotency.CancelRequests[cancel.IdempotencyKey] = cancel.ID
	return nil
}

func (s *RunnerState) UpsertStatusWriteback(writeback StatusWriteback) error {
	s.Normalize()
	if strings.TrimSpace(writeback.IdempotencyKey) == "" {
		return fmt.Errorf("status writeback idempotency key is required")
	}
	if writeback.Status != "" && !writeback.Status.Valid() {
		return fmt.Errorf("invalid writeback status %q", writeback.Status)
	}
	s.StatusWritebacks[writeback.IdempotencyKey] = writeback
	s.Idempotency.StatusWritebacks[writeback.IdempotencyKey] = writeback.IdempotencyKey
	return nil
}

func (s *RunnerState) FindCommandJob(idempotencyKey string) (Job, bool) {
	s.Normalize()
	id := s.Idempotency.CommandJobs[idempotencyKey]
	if id == "" {
		return Job{}, false
	}
	job, ok := s.Jobs[id]
	return job, ok
}

func (s *RunnerState) FindCancellation(idempotencyKey string) (Cancellation, bool) {
	s.Normalize()
	id := s.Idempotency.CancelRequests[idempotencyKey]
	if id == "" {
		return Cancellation{}, false
	}
	cancel, ok := s.Cancellations[id]
	return cancel, ok
}

func (s *RunnerState) FindStatusWriteback(idempotencyKey string) (StatusWriteback, bool) {
	s.Normalize()
	key := s.Idempotency.StatusWritebacks[idempotencyKey]
	if key == "" {
		return StatusWriteback{}, false
	}
	writeback, ok := s.StatusWritebacks[key]
	return writeback, ok
}

func (s LifecycleStatus) Valid() bool {
	switch s {
	case StatusQueued, StatusDispatched, StatusRunning, StatusCompleted, StatusFailed, StatusRejected, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

func (s LifecycleStatus) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusRejected, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

func (s LifecycleStatus) NeedsReconciliation() bool {
	return s == StatusDispatched || s == StatusRunning
}

func canTransition(from, to LifecycleStatus) bool {
	if from == "" {
		return to == StatusQueued || to == StatusRejected
	}
	if from == to {
		return true
	}
	if from.Terminal() {
		return false
	}
	switch from {
	case StatusQueued:
		return to == StatusDispatched || to == StatusRunning || to == StatusFailed || to == StatusRejected || to == StatusCancelled
	case StatusDispatched:
		return to == StatusRunning || to == StatusCompleted || to == StatusFailed || to == StatusCancelled || to == StatusInterrupted
	case StatusRunning:
		return to == StatusCompleted || to == StatusFailed || to == StatusCancelled || to == StatusInterrupted
	default:
		return false
	}
}

func firstTime(existing, fallback time.Time) time.Time {
	if existing.IsZero() {
		return fallback
	}
	return existing
}

func sortJobs(jobs []Job) {
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
}
