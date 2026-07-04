package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
)

const (
	SourceNotification       = "notification"
	SourceRepositoryFallback = "repository_fallback"

	CommandStatusIgnored      = "ignored"
	CommandStatusRejected     = "rejected"
	CommandStatusUnauthorized = "unauthorized"
	CommandStatusDuplicate    = "duplicate"
	CommandStatusJobQueued    = "job_queued"
	CommandStatusCancelQueued = "cancel_queued"

	ReasonSessionNotFound      = "session_not_found"
	ReasonCancellationDisabled = "cancellation_disabled"

	queuedJobReactionContent = "eyes"
)

type Backend interface {
	github.RunnerOperations
	commentrunner.PermissionBackend
}

type NotificationBackend interface {
	PollNotifications(context.Context, github.NotificationListOptions) (github.NotificationListResult, error)
}

type Store interface {
	Load(context.Context) (crstate.RunnerState, error)
	Save(context.Context, crstate.RunnerState) error
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Options struct {
	DryRun              bool
	AuthorizationPolicy commentrunner.AuthorizationPolicy
	NotificationBackend NotificationBackend
	Clock               Clock
}

type runCache struct {
	issueStates        map[string]issueStateCheck
	runnerLogin        string
	runnerLoginChecked bool
	runnerLoginErr     error
}

type issueStateCheck struct {
	open bool
	err  error
}

type Result struct {
	OK            bool              `json:"ok"`
	DryRun        bool              `json:"dry_run"`
	StartedAt     time.Time         `json:"started_at"`
	FinishedAt    time.Time         `json:"finished_at"`
	Notification  NotificationPoll  `json:"notification"`
	Repositories  []RepositoryCycle `json:"repositories,omitempty"`
	Commands      []CommandReport   `json:"commands,omitempty"`
	Jobs          []JobCandidate    `json:"jobs,omitempty"`
	Cancellations []CancelCandidate `json:"cancellations,omitempty"`
	Diagnostics   []Diagnostic      `json:"diagnostics,omitempty"`
	Next          NextStep          `json:"next"`
}

type RepositoryCycle struct {
	Repo                     string       `json:"repo"`
	NotificationSeen         int          `json:"notification_seen,omitempty"`
	NotificationSeenThreads  int          `json:"notification_seen_threads,omitempty"`
	FallbackDue              bool         `json:"fallback_due"`
	FallbackExecuted         bool         `json:"fallback_executed"`
	FallbackNextAt           time.Time    `json:"fallback_next_at,omitempty"`
	RepositoryCommentsCursor CursorReport `json:"repository_comments_cursor,omitempty"`
	FallbackMessage          string       `json:"fallback_message,omitempty"`
}

type NotificationPoll struct {
	Poller                     string       `json:"poller"`
	ConfiguredIdentity         string       `json:"configured_identity,omitempty"`
	TokenEnv                   string       `json:"token_env,omitempty"`
	StatusCode                 int          `json:"status_code,omitempty"`
	NotModified                bool         `json:"not_modified,omitempty"`
	ConditionalETag            bool         `json:"conditional_etag"`
	ConditionalLastModified    bool         `json:"conditional_last_modified"`
	PollIntervalSeconds        int          `json:"poll_interval_seconds,omitempty"`
	MatchedNotifications       int          `json:"matched_notifications"`
	MatchedNotificationThreads int          `json:"matched_notification_threads"`
	MatchedRepositories        []string     `json:"matched_repositories,omitempty"`
	Cursor                     CursorReport `json:"cursor,omitempty"`
	Message                    string       `json:"message,omitempty"`
}

type CursorReport struct {
	Resource             string         `json:"resource,omitempty"`
	LastStatusCode       int            `json:"last_status_code,omitempty"`
	ETagSet              bool           `json:"etag_set"`
	LastModifiedSet      bool           `json:"last_modified_set"`
	CursorURLSet         bool           `json:"cursor_url_set"`
	LastSeenID           int64          `json:"last_seen_id,omitempty"`
	LastSeenAt           time.Time      `json:"last_seen_at,omitempty"`
	LastPollAt           time.Time      `json:"last_poll_at,omitempty"`
	LastSuccessfulPollAt time.Time      `json:"last_successful_poll_at,omitempty"`
	XPollIntervalSeconds int            `json:"x_poll_interval_seconds,omitempty"`
	RateLimit            *RateLimitInfo `json:"rate_limit,omitempty"`
}

type RateLimitInfo struct {
	Remaining         int       `json:"remaining,omitempty"`
	ResetAt           time.Time `json:"reset_at,omitempty"`
	Resource          string    `json:"resource,omitempty"`
	RetryAfterSeconds int       `json:"retry_after_seconds,omitempty"`
}

type CommandReport struct {
	Source            string                            `json:"source"`
	Repo              string                            `json:"repo,omitempty"`
	Issue             int                               `json:"issue,omitempty"`
	CommentID         int64                             `json:"comment_id,omitempty"`
	CommentURL        string                            `json:"comment_url,omitempty"`
	Commenter         string                            `json:"commenter,omitempty"`
	Status            string                            `json:"status"`
	Verb              commentrunner.CommandVerb         `json:"verb,omitempty"`
	CommandID         string                            `json:"command_id,omitempty"`
	JobID             string                            `json:"job_id,omitempty"`
	CancellationID    string                            `json:"cancellation_id,omitempty"`
	PublicSessionID   string                            `json:"public_session_id,omitempty"`
	Created           bool                              `json:"created,omitempty"`
	Reason            string                            `json:"reason,omitempty"`
	Message           string                            `json:"message,omitempty"`
	ParseRejection    commentrunner.CommandRejection    `json:"parse_rejection,omitempty"`
	Authorization     commentrunner.AuthorizationResult `json:"authorization,omitempty"`
	FirstObservedAt   time.Time                         `json:"first_observed_at,omitempty"`
	FirstObservedHash string                            `json:"first_observed_body_hash,omitempty"`
}

type JobCandidate struct {
	JobID           string                    `json:"job_id"`
	CommandID       string                    `json:"command_id"`
	Repo            string                    `json:"repo"`
	Issue           int                       `json:"issue"`
	Verb            commentrunner.CommandVerb `json:"verb"`
	TriggerComment  int64                     `json:"trigger_comment_id"`
	Commenter       string                    `json:"commenter"`
	PublicSessionID string                    `json:"public_session_id,omitempty"`
	Created         bool                      `json:"created"`
}

type CancelCandidate struct {
	CancellationID  string `json:"cancellation_id"`
	Repo            string `json:"repo"`
	TriggerComment  int64  `json:"trigger_comment_id"`
	CancelingUser   string `json:"canceling_user"`
	PublicSessionID string `json:"public_session_id"`
	Created         bool   `json:"created"`
}

type Diagnostic struct {
	Source  string `json:"source,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Issue   int    `json:"issue,omitempty"`
	Message string `json:"message"`
}

type NextStep struct {
	PollAfter        time.Duration `json:"poll_after"`
	PollAt           time.Time     `json:"poll_at"`
	FallbackAfter    time.Duration `json:"fallback_after"`
	FallbackAt       time.Time     `json:"fallback_at,omitempty"`
	Reason           string        `json:"reason,omitempty"`
	RateLimitResetAt time.Time     `json:"rate_limit_reset_at,omitempty"`
}

func RunOnce(ctx context.Context, cfg commentrunner.Config, backend Backend, store Store, opts Options) (Result, error) {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	if backend == nil {
		return Result{}, fmt.Errorf("intake backend is required")
	}
	if store == nil {
		return Result{}, fmt.Errorf("intake state store is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	now := clock.Now().UTC()
	policy := opts.AuthorizationPolicy
	if zeroAuthorizationPolicy(policy) {
		policy = commentrunner.DefaultAuthorizationPolicy()
	}
	if policy.RunnerLogin == "" {
		policy.RunnerLogin = cfg.RunnerIdentity
	}

	loaded, err := store.Load(ctx)
	if err != nil {
		return Result{}, err
	}
	st, err := cloneState(loaded)
	if err != nil {
		return Result{}, err
	}
	st.Normalize()

	result := Result{OK: true, DryRun: opts.DryRun, StartedAt: now}
	repoSet := map[string]bool{}
	for _, repo := range cfg.Repositories {
		repoSet[repo] = true
		ensureRepoState(&st, cfg, repo)
	}
	cache := &runCache{issueStates: map[string]issueStateCheck{}}

	notificationBackend := opts.NotificationBackend
	if notificationBackend == nil {
		notificationBackend = backend
	}
	notificationCursorBefore := notificationCursor(st, cfg.Repositories)
	notifications, notificationMeta, err := pollNotifications(ctx, notificationBackend, st, cfg.Repositories)
	if err != nil {
		if hasResponseMetadata(notificationMeta) {
			applyNotificationMetadata(&st, cfg.Repositories, notificationMeta, now)
		}
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceNotification, Message: err.Error()})
	} else {
		applyNotificationMetadata(&st, cfg.Repositories, notificationMeta, now)
		intakeNotifications(ctx, backend, cfg, policy, &st, cache, notifications, repoSet, now, &result)
	}
	result.Notification = notificationPollReport(cfg, notificationCursorBefore, notificationCursor(st, cfg.Repositories), notificationMeta, notifications, repoSet, err)

	for _, repo := range cfg.Repositories {
		cycle := RepositoryCycle{Repo: repo}
		cycle.NotificationSeen, cycle.NotificationSeenThreads = notificationCountsForRepo(notifications, repo)
		repoState := st.Repositories[repo]
		cycle.FallbackDue = fallbackDue(repoState, now)
		if cycle.FallbackDue {
			cycle.FallbackExecuted = true
			intakeFallback(ctx, backend, cfg, policy, &st, cache, repo, now, &result)
			repoState = st.Repositories[repo]
		}
		cycle.FallbackNextAt = repoState.FallbackCadence.NextPollAt
		cycle.RepositoryCommentsCursor = cursorReport(repoState.RepositoryCommentCursor)
		cycle.FallbackMessage = fallbackCycleMessage(cycle)
		result.Repositories = append(result.Repositories, cycle)
	}

	result.Next = computeNextStep(cfg, st, now)
	result.FinishedAt = clock.Now().UTC()
	if !opts.DryRun {
		if err := store.Save(ctx, st); err != nil {
			return result, err
		}
	}
	return result, nil
}

func cloneState(st crstate.RunnerState) (crstate.RunnerState, error) {
	data, err := json.Marshal(st)
	if err != nil {
		return crstate.RunnerState{}, err
	}
	var out crstate.RunnerState
	if err := json.Unmarshal(data, &out); err != nil {
		return crstate.RunnerState{}, err
	}
	out.Normalize()
	return out, nil
}

func zeroAuthorizationPolicy(policy commentrunner.AuthorizationPolicy) bool {
	return policy.RunnerLogin == "" && len(policy.AllowedUsers) == 0 && !policy.AllowAuthenticatedUser
}

func pollNotifications(ctx context.Context, backend NotificationBackend, st crstate.RunnerState, repos []string) ([]github.Notification, github.ResponseMetadata, error) {
	cursor := notificationCursor(st, repos)
	result, err := backend.PollNotifications(ctx, github.NotificationListOptions{
		ConditionalRequest: conditionalRequestFromCursor(cursor),
		All:                true,
	})
	if err != nil {
		return nil, result.Metadata, err
	}
	if result.Metadata.NotModified {
		return nil, result.Metadata, nil
	}
	return result.Notifications, result.Metadata, nil
}

func intakeNotifications(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, cache *runCache, notifications []github.Notification, repoSet map[string]bool, now time.Time, result *Result) {
	seenThreads := map[string]bool{}
	for _, notification := range notifications {
		repo, ok := repoFromSet(notification.Repository.FullName, repoSet)
		if !ok {
			continue
		}
		issueNumber := notificationIssueNumber(notification)
		if issueNumber <= 0 {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceNotification, Repo: repo, Message: "notification subject did not contain an issue or pull request number"})
			continue
		}
		key := repo + "#" + strconv.Itoa(issueNumber)
		if seenThreads[key] {
			continue
		}
		seenThreads[key] = true
		if !cache.issueOpen(ctx, backend, repo, issueNumber, SourceNotification, result) {
			continue
		}
		intakeIssueComments(ctx, backend, cfg, policy, st, cache, repo, issueNumber, SourceNotification, now, result)
	}
}

func intakeIssueComments(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, cache *runCache, repo string, issueNumber int, source string, now time.Time, result *Result) {
	repoState := st.Repositories[repo]
	cursorKey := strconv.Itoa(issueNumber)
	cursor := repoState.NotificationThreadCursors[cursorKey]
	lastSuccessfulPollAt := cursor.LastSuccessfulPollAt
	pendingCursor := cursor
	page := github.RunnerPageOptions{}
	for {
		commentsResult, err := backend.ListIssueCommentsPage(ctx, repo, issueNumber, github.CommentListOptions{
			ConditionalRequest: conditionalRequestFromCursor(cursor),
			Page:               page,
		})
		if err != nil {
			if hasResponseMetadata(commentsResult.Metadata) {
				if cursor.Cursor == "" && page.CursorURL != "" {
					cursor.Cursor = page.CursorURL
				}
				cursor = updateCursorErrorMetadata(cursor, fmt.Sprintf("issue-comments:%s#%d", repo, issueNumber), commentsResult.Metadata, now)
				cursor.LastSuccessfulPollAt = lastSuccessfulPollAt
				repoState = st.Repositories[repo]
				repoState.NotificationThreadCursors[cursorKey] = cursor
				st.Repositories[repo] = repoState
			}
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: source, Repo: repo, Issue: issueNumber, Message: err.Error()})
			return
		}
		nextURL := commentsResult.Metadata.Pagination.NextURL
		if commentsResult.Metadata.NotModified && nextURL == "" && page.CursorURL == "" && cursor.Cursor != "" {
			nextURL = cursor.Cursor
		}
		pendingCursor = updateCursor(pendingCursor, fmt.Sprintf("issue-comments:%s#%d", repo, issueNumber), commentsResult.Metadata, now)
		if !commentsResult.Metadata.NotModified {
			for _, comment := range commentsResult.Comments {
				if comment.IssueNumber == 0 {
					comment.IssueNumber = issueNumber
				}
				processComment(ctx, backend, cfg, policy, st, cache, repo, comment, source, now, result)
			}
		}
		if nextURL == "" {
			repoState = st.Repositories[repo]
			repoState.NotificationThreadCursors[cursorKey] = pendingCursor
			st.Repositories[repo] = repoState
			return
		}
		page = github.RunnerPageOptions{CursorURL: nextURL}
		cursor = pendingCursor
	}
}

func intakeFallback(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, cache *runCache, repo string, now time.Time, result *Result) {
	repoState := st.Repositories[repo]
	cursor := repoState.RepositoryCommentCursor
	page := github.RunnerPageOptions{}
	for {
		commentsResult, err := backend.ListRepositoryIssueCommentsPage(ctx, repo, github.CommentListOptions{
			ConditionalRequest: conditionalRequestFromCursor(cursor),
			Page:               page,
			Since:              sinceFromCursor(cursor),
		})
		if err != nil {
			if hasResponseMetadata(commentsResult.Metadata) {
				cursor = updateCursor(cursor, "repo-comments:"+repo, commentsResult.Metadata, now)
				repoState.RepositoryCommentCursor = cursor
				st.Repositories[repo] = repoState
			}
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceRepositoryFallback, Repo: repo, Message: err.Error()})
			break
		}
		cursor = updateCursor(cursor, "repo-comments:"+repo, commentsResult.Metadata, now)
		if !commentsResult.Metadata.NotModified {
			for _, comment := range commentsResult.Comments {
				issueNumber := comment.IssueNumber
				if issueNumber == 0 {
					issueNumber = issueNumberFromURL(comment.IssueURL)
					comment.IssueNumber = issueNumber
				}
				if issueNumber <= 0 {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceRepositoryFallback, Repo: repo, Message: fmt.Sprintf("comment %d did not include an issue number", comment.ID)})
					continue
				}
				if issue := cache.issueState(ctx, backend, repo, issueNumber, SourceRepositoryFallback, result); !issue.open {
					if issue.err == nil {
						if comment.ID > cursor.LastSeenID {
							cursor.LastSeenID = comment.ID
						}
						if comment.UpdatedAt.After(cursor.LastSeenAt) {
							cursor.LastSeenAt = comment.UpdatedAt.UTC()
						}
					}
					continue
				}
				processComment(ctx, backend, cfg, policy, st, cache, repo, comment, SourceRepositoryFallback, now, result)
				if comment.ID > cursor.LastSeenID {
					cursor.LastSeenID = comment.ID
				}
				if comment.UpdatedAt.After(cursor.LastSeenAt) {
					cursor.LastSeenAt = comment.UpdatedAt.UTC()
				}
			}
		}
		if commentsResult.Metadata.Pagination.NextURL == "" {
			break
		}
		page = github.RunnerPageOptions{CursorURL: commentsResult.Metadata.Pagination.NextURL}
	}
	repoState = st.Repositories[repo]
	repoState.RepositoryCommentCursor = cursor
	repoState.FallbackCadence = crstate.FallbackCadence{
		Enabled:         true,
		IntervalSeconds: int(cfg.FallbackInterval.Duration.Seconds()),
		LastFallbackAt:  now,
		NextPollAt:      now.Add(cfg.FallbackInterval.Duration),
	}
	st.Repositories[repo] = repoState
}

func processComment(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, cache *runCache, repo string, comment github.Comment, source string, now time.Time, result *Result) {
	commenter := ""
	if comment.User != nil {
		commenter = comment.User.Login
	}
	trigger := commentrunner.TriggerComment{
		Repo:       repo,
		Issue:      comment.IssueNumber,
		CommentID:  comment.ID,
		CommentURL: comment.HTMLURL,
		Body:       comment.Body,
		Commenter:  commenter,
		UpdatedAt:  comment.UpdatedAt,
		ObservedAt: now,
	}
	seen := seenCommentFromTrigger(cfg, trigger, comment.URL)
	parse := commentrunner.ParseCommandComment(trigger)
	switch parse.Status {
	case commentrunner.ParseStatusIgnored:
		result.Commands = append(result.Commands, baseReport(source, trigger, CommandStatusIgnored))
	case commentrunner.ParseStatusRejected:
		report := baseReport(source, trigger, CommandStatusRejected)
		report.ParseRejection = parse.Rejection
		report.Reason = string(parse.Rejection.Reason)
		report.Message = parse.Rejection.Message
		writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
		result.Commands = append(result.Commands, report)
	case commentrunner.ParseStatusAccepted:
		if runnerAcked, err := commentHasRunnerEyesAck(ctx, backend, cfg, policy, cache, repo, comment); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{
				Source:  source,
				Repo:    repo,
				Issue:   comment.IssueNumber,
				Message: "remote command ack check: " + boundedOneLine(err.Error(), 512),
			})
		} else if runnerAcked {
			report := candidateReport(source, parse.Candidate, CommandStatusDuplicate)
			report.Reason = "remote_runner_ack"
			report.Message = "comment already has an eyes reaction from the runner identity"
			result.Commands = append(result.Commands, report)
			return
		}
		processCandidate(ctx, backend, cfg, policy, st, seen, parse.Candidate, source, now, result)
	}
}

func seenCommentFromTrigger(cfg commentrunner.Config, trigger commentrunner.TriggerComment, apiURL string) crstate.SeenComment {
	return crstate.SeenComment{
		Host:                   cfg.Hostname,
		Repo:                   trigger.Repo,
		IssueNumber:            trigger.Issue,
		CommentID:              trigger.CommentID,
		HTMLURL:                trigger.CommentURL,
		APIURL:                 apiURL,
		AuthorLogin:            trigger.Commenter,
		FirstObservedAt:        trigger.ObservedAt,
		FirstObservedUpdatedAt: trigger.UpdatedAt,
		FirstObservedBodyHash:  commentrunner.BodyHash(trigger.Body),
	}
}

func processCandidate(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, seen crstate.SeenComment, candidate commentrunner.CommandCandidate, source string, now time.Time, result *Result) {
	authRepo := candidate.Repo
	cancelTargetJobID := ""
	if candidate.Verb == commentrunner.VerbResume {
		session, ok := st.GetPublicSession(candidate.Repo, candidate.PublicSessionID)
		if !ok {
			report := candidateReport(source, candidate, CommandStatusRejected)
			report.Reason = ReasonSessionNotFound
			report.Message = "public session id was not found in this repository"
			writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
			result.Commands = append(result.Commands, report)
			return
		}
		authRepo = session.Repo
	}
	if candidate.Verb == commentrunner.VerbCancel {
		if session, ok := st.GetPublicSession(candidate.Repo, candidate.PublicSessionID); ok {
			authRepo = session.Repo
		} else if job, ok := activeCancelTarget(st, candidate.Repo, candidate.PublicSessionID); ok {
			authRepo = job.Repo
			cancelTargetJobID = job.ID
		} else {
			report := candidateReport(source, candidate, CommandStatusRejected)
			report.Reason = ReasonSessionNotFound
			report.Message = "public session id was not found in this repository"
			writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
			result.Commands = append(result.Commands, report)
			return
		}
	}
	authz := commentrunner.AuthorizeCandidateForRepo(ctx, backend, candidate, authRepo, policy)
	if !authz.Allowed {
		report := candidateReport(source, candidate, CommandStatusUnauthorized)
		report.Authorization = authz
		report.Reason = string(authz.Reason)
		report.Message = authz.Message
		writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
		result.Commands = append(result.Commands, report)
		return
	}
	if candidate.Verb == commentrunner.VerbCancel {
		if !cfg.CancellationEnabled {
			report := candidateReport(source, candidate, CommandStatusRejected)
			report.Authorization = authz
			report.Reason = ReasonCancellationDisabled
			report.Message = "runner cancellation is disabled by configuration"
			writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
			result.Commands = append(result.Commands, report)
			return
		}
		queueCancellation(st, seen, candidate, source, authz, cancelTargetJobID, now, result)
		return
	}
	queueJob(ctx, backend, cfg, st, seen, candidate, source, authz, now, result)
}

func queueJob(ctx context.Context, backend Backend, cfg commentrunner.Config, st *crstate.RunnerState, seen crstate.SeenComment, candidate commentrunner.CommandCandidate, source string, authz commentrunner.AuthorizationResult, now time.Time, result *Result) {
	seen.ProducedCommandCandidate = true
	seen.CommandCandidateID = candidate.ID
	seen.CommandName = string(candidate.Verb)
	seen.CommandIdempotencyKey = candidate.IdempotencyKey
	job := crstate.Job{
		ID:                    stableID("job", candidate.IdempotencyKey),
		Repo:                  candidate.Repo,
		IssueNumber:           candidate.Issue,
		PublicSessionID:       candidate.PublicSessionID,
		CoordinatorKind:       cfg.Agent.Kind,
		Model:                 cfg.Agent.Model,
		SessionCreatorLogin:   sessionCreator(candidate),
		TriggeringUserLogin:   candidate.Commenter,
		TriggerCommentID:      candidate.TriggerCommentID,
		CommandID:             candidate.ID,
		CommandName:           string(candidate.Verb),
		CommandPrompt:         candidate.Prompt,
		CommandIdempotencyKey: candidate.IdempotencyKey,
		Status:                crstate.StatusQueued,
		CreatedAt:             now,
		UpdatedAt:             now,
		FirstObservedComment:  seen,
		SourceLabels:          []string{source},
	}
	createdJob, created, err := st.CreateCommandJob(job)
	report := candidateReport(source, candidate, CommandStatusJobQueued)
	report.Authorization = authz
	if err != nil {
		result.OK = false
		report.Status = CommandStatusRejected
		report.Reason = "state_error"
		report.Message = err.Error()
		result.Commands = append(result.Commands, report)
		return
	}

	report.JobID = createdJob.ID
	report.Created = created
	if !created {
		report.Status = CommandStatusDuplicate
		report.Reason = "idempotency_key_exists"
		report.Message = "command job already exists for this idempotency key"
	}
	result.Commands = append(result.Commands, report)
	if !created {
		return
	}
	result.Jobs = append(result.Jobs, JobCandidate{
		JobID:           createdJob.ID,
		CommandID:       candidate.ID,
		Repo:            candidate.Repo,
		Issue:           candidate.Issue,
		Verb:            candidate.Verb,
		TriggerComment:  candidate.TriggerCommentID,
		Commenter:       candidate.Commenter,
		PublicSessionID: candidate.PublicSessionID,
		Created:         created,
	})
	addQueuedJobReaction(ctx, backend, candidate, source, result)
}

func addQueuedJobReaction(ctx context.Context, backend Backend, candidate commentrunner.CommandCandidate, source string, result *Result) {
	if result == nil || result.DryRun {
		return
	}
	if strings.TrimSpace(candidate.Repo) == "" || candidate.TriggerCommentID == 0 {
		return
	}
	if _, err := backend.AddCommentReaction(ctx, candidate.Repo, candidate.TriggerCommentID, queuedJobReactionContent); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Source:  source,
			Repo:    candidate.Repo,
			Issue:   candidate.Issue,
			Message: "queued job reaction: " + boundedOneLine(err.Error(), 512),
		})
	}
}

func queueCancellation(st *crstate.RunnerState, seen crstate.SeenComment, candidate commentrunner.CommandCandidate, source string, authz commentrunner.AuthorizationResult, targetJobID string, now time.Time, result *Result) {
	seen.ProducedCommandCandidate = true
	seen.CommandCandidateID = candidate.ID
	seen.CommandName = string(candidate.Verb)
	seen.CancelIdempotencyKey = candidate.IdempotencyKey
	cancel := crstate.Cancellation{
		ID:                    stableID("cancel", candidate.IdempotencyKey),
		IdempotencyKey:        candidate.IdempotencyKey,
		Repo:                  candidate.Repo,
		TriggerCommentID:      candidate.TriggerCommentID,
		CancelingUserLogin:    candidate.Commenter,
		TargetPublicSessionID: candidate.PublicSessionID,
		TargetJobID:           targetJobID,
		Status:                crstate.StatusQueued,
		CreatedAt:             now,
	}
	created := true
	if existing, ok := st.FindCancellation(candidate.IdempotencyKey); ok {
		cancel = existing
		created = false
	} else if err := st.UpsertCancellation(cancel); err != nil {
		result.OK = false
		report := candidateReport(source, candidate, CommandStatusRejected)
		report.Authorization = authz
		report.Reason = "state_error"
		report.Message = err.Error()
		result.Commands = append(result.Commands, report)
		return
	}

	report := candidateReport(source, candidate, CommandStatusCancelQueued)
	report.Authorization = authz
	report.CancellationID = cancel.ID
	report.Created = created
	if !created {
		report.Status = CommandStatusDuplicate
		report.Reason = "idempotency_key_exists"
		report.Message = "cancellation already exists for this idempotency key"
	}
	result.Commands = append(result.Commands, report)
	if !created {
		return
	}
	result.Cancellations = append(result.Cancellations, CancelCandidate{
		CancellationID:  cancel.ID,
		Repo:            candidate.Repo,
		TriggerComment:  candidate.TriggerCommentID,
		CancelingUser:   candidate.Commenter,
		PublicSessionID: candidate.PublicSessionID,
		Created:         created,
	})
}

func activeCancelTarget(st *crstate.RunnerState, repo, publicID string) (crstate.Job, bool) {
	if st == nil || strings.TrimSpace(repo) == "" || strings.TrimSpace(publicID) == "" {
		return crstate.Job{}, false
	}
	for _, status := range []crstate.LifecycleStatus{crstate.StatusRunning, crstate.StatusDispatched, crstate.StatusQueued} {
		for _, job := range st.ListJobs() {
			if job.Repo == repo && job.PublicSessionID == publicID && job.Status == status {
				return job, true
			}
		}
	}
	return crstate.Job{}, false
}

func (c *runCache) issueOpen(ctx context.Context, backend Backend, repo string, issueNumber int, source string, result *Result) bool {
	return c.issueState(ctx, backend, repo, issueNumber, source, result).open
}

func (c *runCache) issueState(ctx context.Context, backend Backend, repo string, issueNumber int, source string, result *Result) issueStateCheck {
	if c == nil {
		c = &runCache{issueStates: map[string]issueStateCheck{}}
	}
	if c.issueStates == nil {
		c.issueStates = map[string]issueStateCheck{}
	}
	key := repo + "#" + strconv.Itoa(issueNumber)
	if check, ok := c.issueStates[key]; ok {
		return check
	}
	issue, err := backend.GetIssueContext(ctx, repo, issueNumber, github.ConditionalRequest{})
	check := issueStateCheck{open: err == nil && strings.EqualFold(strings.TrimSpace(issue.Issue.State), "open"), err: err}
	c.issueStates[key] = check
	if err != nil && result != nil {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Source:  source,
			Repo:    repo,
			Issue:   issueNumber,
			Message: "issue context: " + boundedOneLine(err.Error(), 512),
		})
	}
	return check
}

func commentHasRunnerEyesAck(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, cache *runCache, repo string, comment github.Comment) (bool, error) {
	if comment.Reactions.TotalCount <= 0 && comment.Reactions.Eyes <= 0 {
		return false, nil
	}
	runnerLogin, err := cache.runnerIdentity(ctx, backend, cfg, policy)
	if err != nil {
		return false, err
	}
	page := github.RunnerPageOptions{}
	for {
		result, err := backend.ListCommentReactionsPage(ctx, repo, comment.ID, page)
		if err != nil {
			return false, err
		}
		for _, reaction := range result.Reactions {
			if reaction.User == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(reaction.Content), queuedJobReactionContent) && strings.EqualFold(strings.TrimSpace(reaction.User.Login), runnerLogin) {
				return true, nil
			}
		}
		if result.Metadata.Pagination.NextURL == "" {
			return false, nil
		}
		page = github.RunnerPageOptions{CursorURL: result.Metadata.Pagination.NextURL}
	}
}

func (c *runCache) runnerIdentity(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy) (string, error) {
	if c != nil && c.runnerLoginChecked {
		return c.runnerLogin, c.runnerLoginErr
	}
	user, _, err := backend.GetUser(ctx)
	if err == nil && strings.TrimSpace(user.Login) == "" {
		err = fmt.Errorf("runner identity lookup returned an empty login")
	}
	if err == nil {
		expected := strings.TrimSpace(policy.RunnerLogin)
		if expected == "" {
			expected = strings.TrimSpace(cfg.RunnerIdentity)
		}
		if expected != "" && !strings.EqualFold(expected, strings.TrimSpace(user.Login)) {
			err = fmt.Errorf("authenticated runner identity %q does not match configured runner %q", user.Login, expected)
		}
	}
	if c != nil {
		c.runnerLoginChecked = true
		c.runnerLoginErr = err
		if err == nil {
			c.runnerLogin = strings.TrimSpace(user.Login)
		}
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(user.Login), nil
}

func writeRejectedCommand(ctx context.Context, backend Backend, cfg commentrunner.Config, st *crstate.RunnerState, seen crstate.SeenComment, report CommandReport, now time.Time, result *Result) {
	if result == nil || result.DryRun || st == nil {
		return
	}
	if strings.TrimSpace(report.Repo) == "" || report.Issue <= 0 || report.CommentID == 0 {
		return
	}
	key := rejectedStatusWritebackKey(report)
	seen.StatusWritebackIdempotencyKey = key
	if report.CommandID != "" {
		seen.ProducedCommandCandidate = true
		seen.CommandCandidateID = report.CommandID
		seen.CommandName = string(report.Verb)
	}

	job := rejectedWritebackJob(cfg, seen, report, key, now)
	service := &writeback.Service{
		GitHub: backend,
		Store:  runnerStateWritebackStore{state: st},
		Clock:  func() time.Time { return now },
	}
	if _, err := service.Write(ctx, writeback.Request{
		Job:         job,
		Status:      crstate.StatusRejected,
		Phase:       rejectedPhase(report),
		Diagnostics: []string{rejectedDiagnostic(report)},
	}); err != nil {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Source:  report.Source,
			Repo:    report.Repo,
			Issue:   report.Issue,
			Message: "rejected command writeback: " + boundedOneLine(err.Error(), 512),
		})
	}
}

type runnerStateWritebackStore struct {
	state *crstate.RunnerState
}

func (s runnerStateWritebackStore) Update(ctx context.Context, mutate func(*crstate.RunnerState) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.state == nil {
		return fmt.Errorf("intake writeback state is required")
	}
	if mutate == nil {
		return nil
	}
	if err := mutate(s.state); err != nil {
		return err
	}
	s.state.Normalize()
	return nil
}

func rejectedWritebackJob(cfg commentrunner.Config, seen crstate.SeenComment, report CommandReport, key string, now time.Time) crstate.Job {
	commandName := string(report.Verb)
	if commandName == "" {
		commandName = "rejected"
	}
	commandID := report.CommandID
	if commandID == "" {
		commandID = stableID("cmd", key)
	}
	createdAt := report.FirstObservedAt
	if createdAt.IsZero() {
		createdAt = seen.FirstObservedAt
	}
	if createdAt.IsZero() {
		createdAt = now
	}
	seen.StatusWritebackIdempotencyKey = key
	return crstate.Job{
		ID:                   stableID("job", key),
		Repo:                 report.Repo,
		IssueNumber:          report.Issue,
		PublicSessionID:      report.PublicSessionID,
		CoordinatorKind:      cfg.Agent.Kind,
		Model:                cfg.Agent.Model,
		TriggeringUserLogin:  report.Commenter,
		TriggerCommentID:     report.CommentID,
		CommandID:            commandID,
		CommandName:          commandName,
		StatusWritebackKey:   key,
		Status:               crstate.StatusRejected,
		CreatedAt:            createdAt,
		UpdatedAt:            now,
		FinishedAt:           now,
		FirstObservedComment: seen,
		SourceLabels:         []string{report.Source},
		Diagnostics:          []string{rejectedDiagnostic(report)},
	}
}

func rejectedStatusWritebackKey(report CommandReport) string {
	base := fmt.Sprintf("rejected-command-v1:%s:%d:%d:%s:%s", report.Repo, report.Issue, report.CommentID, report.Status, report.Reason)
	return "status:" + stableID("rejected", base)
}

func rejectedPhase(report CommandReport) string {
	switch {
	case report.Status == CommandStatusUnauthorized:
		return "command-unauthorized"
	case report.Reason == ReasonSessionNotFound:
		return "unknown-session"
	case report.Reason == ReasonCancellationDisabled:
		return "cancellation-disabled"
	default:
		return "command-rejected"
	}
}

func rejectedDiagnostic(report CommandReport) string {
	parts := []string{"command " + report.Status}
	if report.Reason != "" {
		parts = append(parts, "reason="+report.Reason)
	}
	if report.Authorization.Reason != "" {
		parts = append(parts, "auth="+string(report.Authorization.Reason))
	}
	if report.Message != "" {
		parts = append(parts, "message="+report.Message)
	}
	return boundedOneLine(strings.Join(parts, "; "), 512)
}

func boundedOneLine(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	for len([]byte(value)) > maxBytes-3 {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return "..."
		}
		value = value[:len(value)-size]
	}
	return value + "..."
}

func ensureRepoState(st *crstate.RunnerState, cfg commentrunner.Config, repo string) {
	repoState := st.Repositories[repo]
	repoState.Host = cfg.Hostname
	repoState.Repo = repo
	repoState.Backend = string(cfg.GitHubBackend)
	repoState.RunnerLogin = cfg.RunnerIdentity
	if repoState.IssueCommentCursors == nil {
		repoState.IssueCommentCursors = map[string]crstate.CursorState{}
	}
	if repoState.NotificationThreadCursors == nil {
		repoState.NotificationThreadCursors = map[string]crstate.CursorState{}
	}
	if !repoState.FallbackCadence.Enabled {
		repoState.FallbackCadence.Enabled = true
		repoState.FallbackCadence.IntervalSeconds = int(cfg.FallbackInterval.Duration.Seconds())
	}
	st.Repositories[repo] = repoState
}

func notificationCursor(st crstate.RunnerState, repos []string) crstate.CursorState {
	for _, repo := range repos {
		cursor := st.Repositories[repo].NotificationCursor
		if cursor.ETag != "" || cursor.LastModified != "" {
			return cursor
		}
	}
	return crstate.CursorState{}
}

func notificationPollReport(cfg commentrunner.Config, cursorBefore, cursorAfter crstate.CursorState, meta github.ResponseMetadata, notifications []github.Notification, repoSet map[string]bool, pollErr error) NotificationPoll {
	cfg = cfg.Normalized()
	report := NotificationPoll{
		Poller:                  "main_runner",
		ConfiguredIdentity:      cfg.RunnerIdentity,
		StatusCode:              meta.StatusCode,
		NotModified:             meta.NotModified,
		ConditionalETag:         cursorETag(cursorBefore.ETag) != "",
		ConditionalLastModified: strings.TrimSpace(cursorBefore.LastModified) != "",
		PollIntervalSeconds:     meta.PollIntervalSeconds,
		Cursor:                  cursorReport(cursorAfter),
	}
	if cfg.NotificationTokenEnv != "" {
		report.Poller = "notification_runner"
		report.ConfiguredIdentity = cfg.NotificationIdentity
		report.TokenEnv = cfg.NotificationTokenEnv
	}
	report.MatchedNotifications, report.MatchedNotificationThreads, report.MatchedRepositories = notificationMatchCounts(notifications, repoSet)
	report.Message = notificationPollMessage(report, pollErr)
	return report
}

func notificationMatchCounts(notifications []github.Notification, repoSet map[string]bool) (int, int, []string) {
	seenRepos := map[string]bool{}
	seenThreads := map[string]bool{}
	for _, notification := range notifications {
		repo, ok := repoFromSet(notification.Repository.FullName, repoSet)
		if !ok {
			continue
		}
		seenRepos[repo] = true
		issueNumber := notificationIssueNumber(notification)
		if issueNumber <= 0 {
			continue
		}
		seenThreads[strings.ToLower(repo)+"#"+strconv.Itoa(issueNumber)] = true
	}
	var repos []string
	for repo := range seenRepos {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return len(matchedNotifications(notifications, repoSet)), len(seenThreads), repos
}

func matchedNotifications(notifications []github.Notification, repoSet map[string]bool) []github.Notification {
	var matched []github.Notification
	for _, notification := range notifications {
		if _, ok := repoFromSet(notification.Repository.FullName, repoSet); ok {
			matched = append(matched, notification)
		}
	}
	return matched
}

func notificationCountsForRepo(notifications []github.Notification, repo string) (int, int) {
	seenThreads := map[int]bool{}
	count := 0
	for _, notification := range notifications {
		if !strings.EqualFold(strings.TrimSpace(notification.Repository.FullName), repo) {
			continue
		}
		count++
		if issueNumber := notificationIssueNumber(notification); issueNumber > 0 {
			seenThreads[issueNumber] = true
		}
	}
	return count, len(seenThreads)
}

func repoFromSet(repo string, repoSet map[string]bool) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repoSet[repo] {
		return repo, true
	}
	for configured := range repoSet {
		if strings.EqualFold(configured, repo) {
			return configured, true
		}
	}
	return "", false
}

func notificationPollMessage(report NotificationPoll, pollErr error) string {
	if pollErr != nil {
		return "notification poll failed; repository comments fallback runs only for repositories whose fallback interval is due"
	}
	if report.NotModified {
		return "HTTP 304 Not Modified means GitHub reported no notification changes for this poller and stored cursor; repository comments fallback still runs when due"
	}
	if report.StatusCode == 0 {
		return "notification poll completed without HTTP metadata"
	}
	if report.MatchedNotifications == 0 {
		return "notification poll returned no matching notifications for configured repositories"
	}
	return "notification poll returned matching notifications; unique issue and pull request threads will be scanned once"
}

func cursorReport(cursor crstate.CursorState) CursorReport {
	var rateLimit *RateLimitInfo
	if cursor.RateLimit.Remaining != 0 ||
		!cursor.RateLimit.ResetAt.IsZero() ||
		cursor.RateLimit.Resource != "" ||
		cursor.RateLimit.RetryAfterSeconds > 0 {
		rateLimit = &RateLimitInfo{
			Remaining:         cursor.RateLimit.Remaining,
			ResetAt:           cursor.RateLimit.ResetAt,
			Resource:          cursor.RateLimit.Resource,
			RetryAfterSeconds: cursor.RateLimit.RetryAfterSeconds,
		}
	}
	return CursorReport{
		Resource:             cursor.Resource,
		LastStatusCode:       cursor.LastStatusCode,
		ETagSet:              cursorETag(cursor.ETag) != "",
		LastModifiedSet:      strings.TrimSpace(cursor.LastModified) != "",
		CursorURLSet:         strings.TrimSpace(cursor.Cursor) != "",
		LastSeenID:           cursor.LastSeenID,
		LastSeenAt:           cursor.LastSeenAt,
		LastPollAt:           cursor.LastPollAt,
		LastSuccessfulPollAt: cursor.LastSuccessfulPollAt,
		XPollIntervalSeconds: cursor.XPollIntervalSeconds,
		RateLimit:            rateLimit,
	}
}

func conditionalRequestFromCursor(cursor crstate.CursorState) github.ConditionalRequest {
	return github.ConditionalRequest{
		ETag:         cursorETag(cursor.ETag),
		LastModified: cursor.LastModified,
	}
}

func cursorETag(value string) string {
	value = strings.TrimSpace(value)
	if value == `""` || strings.EqualFold(value, `W/""`) {
		return ""
	}
	return value
}

func fallbackCycleMessage(cycle RepositoryCycle) string {
	if cycle.FallbackExecuted {
		return "repository comments fallback executed because fallback interval was due"
	}
	return "repository comments fallback skipped until fallback_next_at"
}

func applyNotificationMetadata(st *crstate.RunnerState, repos []string, meta github.ResponseMetadata, now time.Time) {
	for _, repo := range repos {
		repoState := st.Repositories[repo]
		repoState.NotificationCursor = updateCursor(repoState.NotificationCursor, "notifications", meta, now)
		st.Repositories[repo] = repoState
	}
}

func updateCursor(cursor crstate.CursorState, resource string, meta github.ResponseMetadata, now time.Time) crstate.CursorState {
	cursor.Resource = resource
	cursor.ETag = cursorETag(cursor.ETag)
	cursor.LastPollAt = now
	cursor.LastStatusCode = meta.StatusCode
	cursor.RateLimit = rateLimit(meta)
	rawETag := meta.ETag
	if strings.TrimSpace(rawETag) == "" && meta.Headers != nil {
		rawETag = meta.Headers.Get("ETag")
	}
	if strings.TrimSpace(rawETag) != "" {
		cursor.ETag = cursorETag(rawETag)
	}
	if meta.LastModified != "" {
		cursor.LastModified = meta.LastModified
	}
	if meta.PollIntervalSeconds > 0 {
		cursor.XPollIntervalSeconds = meta.PollIntervalSeconds
	}
	if meta.Pagination.NextURL != "" {
		cursor.Cursor = meta.Pagination.NextURL
	} else {
		cursor.Cursor = ""
	}
	if !meta.NotModified && (meta.StatusCode == 0 || meta.StatusCode < 400) {
		cursor.LastSuccessfulPollAt = now
	}
	return cursor
}

func updateCursorErrorMetadata(cursor crstate.CursorState, resource string, meta github.ResponseMetadata, now time.Time) crstate.CursorState {
	cursor.Resource = resource
	cursor.ETag = cursorETag(cursor.ETag)
	cursor.LastPollAt = now
	cursor.LastStatusCode = meta.StatusCode
	cursor.RateLimit = rateLimit(meta)
	if meta.PollIntervalSeconds > 0 {
		cursor.XPollIntervalSeconds = meta.PollIntervalSeconds
	}
	return cursor
}

func rateLimit(meta github.ResponseMetadata) crstate.RateLimitState {
	return crstate.RateLimitState{
		Limit:             meta.RateLimit.Limit,
		Remaining:         meta.RateLimit.Remaining,
		ResetAt:           meta.RateLimit.ResetAt,
		Resource:          meta.RateLimit.Resource,
		RetryAfterSeconds: meta.RateLimit.RetryAfterSeconds,
	}
}

func hasResponseMetadata(meta github.ResponseMetadata) bool {
	return meta.StatusCode != 0 ||
		meta.ETag != "" ||
		meta.LastModified != "" ||
		meta.PollIntervalSeconds > 0 ||
		meta.RateLimit.Limit != 0 ||
		meta.RateLimit.Remaining != 0 ||
		meta.RateLimit.Used != 0 ||
		meta.RateLimit.ResetUnix != 0 ||
		!meta.RateLimit.ResetAt.IsZero() ||
		meta.RateLimit.Resource != "" ||
		meta.RateLimit.RetryAfterSeconds > 0 ||
		meta.Pagination.NextURL != "" ||
		meta.Pagination.PrevURL != "" ||
		meta.Pagination.FirstURL != "" ||
		meta.Pagination.LastURL != ""
}

func fallbackDue(repoState crstate.RepositoryState, now time.Time) bool {
	next := repoState.FallbackCadence.NextPollAt
	return next.IsZero() || !next.After(now)
}

func sinceFromCursor(cursor crstate.CursorState) *time.Time {
	if cursor.LastSeenAt.IsZero() {
		return nil
	}
	since := cursor.LastSeenAt.UTC()
	return &since
}

func computeNextStep(cfg commentrunner.Config, st crstate.RunnerState, now time.Time) NextStep {
	pollAfter := cfg.PollInterval.Duration
	var resetAt time.Time
	for _, repo := range cfg.Repositories {
		repoState := st.Repositories[repo]
		applyCursorBackoff(repoState.NotificationCursor, now, &pollAfter, &resetAt)
		applyCursorBackoff(repoState.RepositoryCommentCursor, now, &pollAfter, &resetAt)
		for _, cursor := range repoState.NotificationThreadCursors {
			applyCursorBackoff(cursor, now, &pollAfter, &resetAt)
		}
		for _, cursor := range repoState.IssueCommentCursors {
			applyCursorBackoff(cursor, now, &pollAfter, &resetAt)
		}
	}
	nextFallback := time.Time{}
	for _, repo := range cfg.Repositories {
		candidate := st.Repositories[repo].FallbackCadence.NextPollAt
		if candidate.IsZero() {
			continue
		}
		if nextFallback.IsZero() || candidate.Before(nextFallback) {
			nextFallback = candidate
		}
	}
	return NextStep{
		PollAfter:        pollAfter,
		PollAt:           now.Add(pollAfter),
		FallbackAfter:    cfg.FallbackInterval.Duration,
		FallbackAt:       nextFallback,
		Reason:           "poll interval, X-Poll-Interval, rate-limit reset, and Retry-After metadata evaluated",
		RateLimitResetAt: resetAt,
	}
}

func applyCursorBackoff(cursor crstate.CursorState, now time.Time, pollAfter *time.Duration, resetAt *time.Time) {
	if cursor.XPollIntervalSeconds > 0 {
		headerAfter := time.Duration(cursor.XPollIntervalSeconds) * time.Second
		if headerAfter > *pollAfter {
			*pollAfter = headerAfter
			*resetAt = time.Time{}
		}
	}
	if cursor.RateLimit.RetryAfterSeconds > 0 {
		retryAt := now.Add(time.Duration(cursor.RateLimit.RetryAfterSeconds) * time.Second)
		if !cursor.LastPollAt.IsZero() {
			retryAt = cursor.LastPollAt.Add(time.Duration(cursor.RateLimit.RetryAfterSeconds) * time.Second)
		}
		if retryAt.After(now) {
			wait := retryAt.Sub(now)
			if wait > *pollAfter {
				*pollAfter = wait
				*resetAt = time.Time{}
			}
		}
	}
	if cursor.RateLimit.Remaining == 0 && !cursor.RateLimit.ResetAt.IsZero() && cursor.RateLimit.ResetAt.After(now) {
		wait := cursor.RateLimit.ResetAt.Sub(now)
		if wait > *pollAfter {
			*pollAfter = wait
			*resetAt = cursor.RateLimit.ResetAt
		}
	}
}

func baseReport(source string, trigger commentrunner.TriggerComment, status string) CommandReport {
	return CommandReport{
		Source:            source,
		Repo:              trigger.Repo,
		Issue:             trigger.Issue,
		CommentID:         trigger.CommentID,
		CommentURL:        trigger.CommentURL,
		Commenter:         trigger.Commenter,
		Status:            status,
		FirstObservedAt:   trigger.ObservedAt,
		FirstObservedHash: commentrunner.BodyHash(trigger.Body),
	}
}

func candidateReport(source string, candidate commentrunner.CommandCandidate, status string) CommandReport {
	return CommandReport{
		Source:            source,
		Repo:              candidate.Repo,
		Issue:             candidate.Issue,
		CommentID:         candidate.TriggerCommentID,
		CommentURL:        candidate.CommentURL,
		Commenter:         candidate.Commenter,
		Status:            status,
		Verb:              candidate.Verb,
		CommandID:         candidate.ID,
		PublicSessionID:   candidate.PublicSessionID,
		FirstObservedAt:   candidate.FirstObservedAt,
		FirstObservedHash: candidate.FirstObservedBodyHash,
	}
}

func sessionCreator(candidate commentrunner.CommandCandidate) string {
	if candidate.Verb == commentrunner.VerbNew {
		return candidate.Commenter
	}
	return ""
}

func notificationIssueNumber(notification github.Notification) int {
	for _, raw := range []string{
		notification.Subject.URL,
		notification.Subject.LatestCommentURL,
		notification.URL,
	} {
		if n := issueNumberFromURL(raw); n > 0 {
			return n
		}
	}
	return 0
}

func issueNumberFromURL(raw string) int {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "issues", "pulls":
			n, err := strconv.Atoi(parts[i+1])
			if err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func stableID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(sum[:])[:16]
}
