package writeback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/templates"
)

type Store interface {
	Update(context.Context, func(*state.RunnerState) error) error
}

type Service struct {
	GitHub github.RunnerOperations
	Store  Store
	Clock  func() time.Time
}

type Request struct {
	Job                  state.Job
	Status               state.LifecycleStatus
	Phase                string
	CoordinatorSummary   *runnercontext.CoordinatorSummary
	CoordinatorReplyBody string
	Diagnostics          []string
	Err                  error
	CancelingUserLogin   string
}

type Result struct {
	Comment   github.Comment
	Writeback state.StatusWriteback
	Metadata  github.ResponseMetadata
	Body      string
	Created   bool
	Updated   bool
	Recovered bool
}

func (s *Service) Write(ctx context.Context, req Request) (Result, error) {
	if err := s.validate(req); err != nil {
		return Result{}, err
	}
	status := writebackStatus(req)
	key := statusWritebackKey(req.Job)
	now := s.now()

	summary, err := requestSummary(req)
	if err != nil {
		return Result{}, err
	}
	req.CoordinatorSummary = summary

	writeback, err := s.prepare(ctx, req, key, status, now)
	if err != nil {
		return Result{}, err
	}

	recovered := false
	if writeback.CommentID == 0 {
		comment, ok, err := s.recoverComment(ctx, req.Job, key)
		if err != nil {
			_ = s.persistFailure(ctx, req.Job, key, status, now, err)
			return Result{}, err
		}
		if ok {
			recovered = true
			writeback.CommentID = comment.ID
			writeback.URL = comment.HTMLURL
			if err := s.persistCommentID(ctx, req.Job, key, writeback.CommentID, writeback.URL, status, now); err != nil {
				return Result{}, err
			}
		}
	}

	body, err := renderBody(req, key, status, writeback.CommentID)
	if err != nil {
		return Result{}, err
	}

	var ghResult github.RunnerCommentResult
	var opErr error
	created := false
	if writeback.CommentID == 0 {
		created = true
		ghResult, opErr = s.GitHub.CreateRunnerComment(ctx, req.Job.Repo, req.Job.IssueNumber, body)
	} else {
		ghResult, opErr = s.GitHub.UpdateRunnerComment(ctx, req.Job.Repo, writeback.CommentID, body)
	}
	if opErr != nil {
		_ = s.persistFailure(ctx, req.Job, key, status, now, opErr)
		return Result{Metadata: ghResult.Metadata, Body: body}, opErr
	}

	final, err := s.persistSuccess(ctx, req.Job, key, status, now, ghResult.Comment)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Comment:   ghResult.Comment,
		Writeback: final,
		Metadata:  ghResult.Metadata,
		Body:      body,
		Created:   created,
		Updated:   !created,
		Recovered: recovered,
	}, nil
}

func (s *Service) validate(req Request) error {
	if s == nil {
		return errors.New("writeback service is required")
	}
	if s.GitHub == nil {
		return errors.New("writeback GitHub runner operations are required")
	}
	if s.Store == nil {
		return errors.New("writeback state store is required")
	}
	if strings.TrimSpace(req.Job.ID) == "" {
		return errors.New("writeback job id is required")
	}
	if strings.TrimSpace(req.Job.Repo) == "" {
		return errors.New("writeback job repo is required")
	}
	if req.Job.IssueNumber <= 0 {
		return errors.New("writeback job issue number is required")
	}
	if !writebackStatus(req).Valid() {
		return fmt.Errorf("invalid writeback status %q", writebackStatus(req))
	}
	if strings.TrimSpace(statusWritebackKey(req.Job)) == "" {
		return errors.New("writeback idempotency key is required")
	}
	return nil
}

func (s *Service) prepare(ctx context.Context, req Request, key string, status state.LifecycleStatus, at time.Time) (state.StatusWriteback, error) {
	var out state.StatusWriteback
	err := s.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		writeback, ok := st.FindStatusWriteback(key)
		if !ok {
			writeback = state.StatusWriteback{IdempotencyKey: key}
		}
		writeback.JobID = req.Job.ID
		writeback.Repo = req.Job.Repo
		writeback.IssueNumber = req.Job.IssueNumber
		writeback.TriggerCommentID = req.Job.TriggerCommentID
		writeback.Status = status
		writeback.LastAttemptAt = at
		writeback.LastError = ""
		if writeback.CommentID == 0 && req.Job.StatusCommentID != 0 {
			writeback.CommentID = req.Job.StatusCommentID
			writeback.URL = req.Job.StatusCommentURL
		}
		if err := st.UpsertStatusWriteback(writeback); err != nil {
			return err
		}
		if err := upsertJobWriteback(st, req.Job, key, writeback.CommentID, writeback.URL); err != nil {
			return err
		}
		out = writeback
		return nil
	})
	return out, err
}

func (s *Service) recoverComment(ctx context.Context, job state.Job, key string) (github.Comment, bool, error) {
	opts := github.CommentListOptions{Page: github.RunnerPageOptions{PerPage: 100}}
	for {
		result, err := s.GitHub.ListIssueCommentsPage(ctx, job.Repo, job.IssueNumber, opts)
		if err != nil {
			return github.Comment{}, false, err
		}
		for _, comment := range result.Comments {
			marker, ok, err := templates.ParseRunnerStatusMarker(comment.Body)
			if err != nil || !ok {
				continue
			}
			if markerMatches(marker, key, job) {
				return comment, true, nil
			}
		}
		if result.Metadata.Pagination.NextURL == "" {
			return github.Comment{}, false, nil
		}
		opts = github.CommentListOptions{Page: github.RunnerPageOptions{CursorURL: result.Metadata.Pagination.NextURL}}
	}
}

func (s *Service) persistCommentID(ctx context.Context, job state.Job, key string, commentID int64, url string, status state.LifecycleStatus, at time.Time) error {
	return s.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		writeback, _ := st.FindStatusWriteback(key)
		writeback.IdempotencyKey = key
		writeback.JobID = job.ID
		writeback.Repo = job.Repo
		writeback.IssueNumber = job.IssueNumber
		writeback.TriggerCommentID = job.TriggerCommentID
		writeback.CommentID = commentID
		writeback.URL = url
		writeback.Status = status
		writeback.LastAttemptAt = at
		writeback.LastError = ""
		if err := st.UpsertStatusWriteback(writeback); err != nil {
			return err
		}
		return upsertJobWriteback(st, job, key, commentID, url)
	})
}

func (s *Service) persistSuccess(ctx context.Context, job state.Job, key string, status state.LifecycleStatus, at time.Time, comment github.Comment) (state.StatusWriteback, error) {
	var out state.StatusWriteback
	err := s.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		writeback, _ := st.FindStatusWriteback(key)
		writeback.IdempotencyKey = key
		writeback.JobID = job.ID
		writeback.Repo = job.Repo
		writeback.IssueNumber = job.IssueNumber
		writeback.TriggerCommentID = job.TriggerCommentID
		writeback.CommentID = comment.ID
		writeback.URL = comment.HTMLURL
		writeback.Status = status
		writeback.LastAttemptAt = at
		writeback.UpdatedAt = at
		writeback.LastError = ""
		if err := st.UpsertStatusWriteback(writeback); err != nil {
			return err
		}
		if err := upsertJobWriteback(st, job, key, comment.ID, comment.HTMLURL); err != nil {
			return err
		}
		out = writeback
		return nil
	})
	return out, err
}

func (s *Service) persistFailure(ctx context.Context, job state.Job, key string, status state.LifecycleStatus, at time.Time, err error) error {
	return s.Store.Update(ctx, func(st *state.RunnerState) error {
		st.Normalize()
		writeback, _ := st.FindStatusWriteback(key)
		writeback.IdempotencyKey = key
		writeback.JobID = job.ID
		writeback.Repo = job.Repo
		writeback.IssueNumber = job.IssueNumber
		writeback.TriggerCommentID = job.TriggerCommentID
		writeback.Status = status
		writeback.LastAttemptAt = at
		writeback.LastError = boundedError(err)
		if storeErr := st.UpsertStatusWriteback(writeback); storeErr != nil {
			return storeErr
		}
		return upsertJobWriteback(st, job, key, writeback.CommentID, writeback.URL)
	})
}

func upsertJobWriteback(st *state.RunnerState, job state.Job, key string, commentID int64, url string) error {
	existing, ok := st.Jobs[job.ID]
	if ok {
		job = existing
	}
	if job.ID == "" {
		return nil
	}
	job.StatusWritebackKey = key
	if commentID != 0 {
		job.StatusCommentID = commentID
		job.StatusCommentURL = url
		job.DispatchIntent.StatusCommentID = commentID
	}
	return st.UpsertJob(job)
}

func renderBody(req Request, key string, status state.LifecycleStatus, commentID int64) (string, error) {
	job := req.Job
	diagnostics := append([]string{}, job.Diagnostics...)
	diagnostics = append(diagnostics, req.Diagnostics...)
	if job.Sandbox.Diagnostics != "" {
		diagnostics = append(diagnostics, "sandbox: "+job.Sandbox.Diagnostics)
	}
	errorText := ""
	if req.Err != nil {
		errorText = boundedError(req.Err)
	}
	return templates.RenderRunnerStatusComment(templates.RunnerStatusComment{
		Marker: templates.RunnerStatusMarker{
			SchemaVersion:       templates.RunnerStatusMarkerSchemaVersion,
			StatusWritebackKey:  key,
			JobID:               job.ID,
			PublicSessionID:     job.PublicSessionID,
			TriggerCommentID:    job.TriggerCommentID,
			TriggeringUserLogin: job.TriggeringUserLogin,
			AgentKind:           job.CoordinatorKind,
			Model:               job.Model,
			StatusCommentID:     commentID,
		},
		Status:              string(status),
		Phase:               req.Phase,
		RunnerJobID:         job.ID,
		PublicSessionID:     job.PublicSessionID,
		TriggerCommentID:    job.TriggerCommentID,
		TriggeringUserLogin: job.TriggeringUserLogin,
		SessionCreatorLogin: job.SessionCreatorLogin,
		CurrentUserLogin:    job.TriggeringUserLogin,
		CancelingUserLogin:  req.CancelingUserLogin,
		AgentKind:           job.CoordinatorKind,
		Model:               job.Model,
		SandboxProvider:     job.Sandbox.SandboxProvider,
		FSBoundary:          job.Sandbox.FSBoundary,
		UnsafeNoSandbox:     job.Sandbox.UnsafeNoSandbox,
		CoordinatorSummary:  req.CoordinatorSummary,
		CLIDirect:           cliDirect(job.CLIDirect),
		Diagnostics:         diagnostics,
		Error:               errorText,
	})
}

func requestSummary(req Request) (*runnercontext.CoordinatorSummary, error) {
	if req.CoordinatorSummary != nil {
		return req.CoordinatorSummary, nil
	}
	if strings.TrimSpace(req.CoordinatorReplyBody) == "" {
		return nil, nil
	}
	summary, found, err := runnercontext.ExtractCoordinatorSummary(req.CoordinatorReplyBody, runnercontext.SummaryBounds{})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &summary, nil
}

func cliDirect(commands []state.CLIDirectProvenance) []templates.RunnerCLICommand {
	out := make([]templates.RunnerCLICommand, 0, len(commands))
	for _, command := range commands {
		line := templates.RunnerCLICommand{
			Name:          command.CommandName,
			ExitCode:      command.ExitCode,
			StdoutSummary: command.StdoutSummary,
			StderrSummary: command.StderrSummary,
			Diagnostics:   command.Diagnostics,
		}
		if len(command.ArtifactRefs) > 0 {
			line.ArtifactID = command.ArtifactRefs[0].ID
			line.ArtifactURL = command.ArtifactRefs[0].URL
		}
		out = append(out, line)
	}
	return out
}

func markerMatches(marker templates.RunnerStatusMarker, key string, job state.Job) bool {
	if marker.StatusWritebackKey != "" && marker.StatusWritebackKey == key {
		return true
	}
	return marker.JobID != "" && marker.JobID == job.ID
}

func writebackStatus(req Request) state.LifecycleStatus {
	if req.Status != "" {
		return req.Status
	}
	if req.Job.Status != "" {
		return req.Job.Status
	}
	return state.StatusQueued
}

func statusWritebackKey(job state.Job) string {
	if strings.TrimSpace(job.StatusWritebackKey) != "" {
		return strings.TrimSpace(job.StatusWritebackKey)
	}
	if strings.TrimSpace(job.FirstObservedComment.StatusWritebackIdempotencyKey) != "" {
		return strings.TrimSpace(job.FirstObservedComment.StatusWritebackIdempotencyKey)
	}
	if strings.TrimSpace(job.CommandIdempotencyKey) != "" {
		return "status:" + strings.TrimSpace(job.CommandIdempotencyKey)
	}
	if strings.TrimSpace(job.Repo) == "" || job.IssueNumber == 0 || job.TriggerCommentID == 0 || strings.TrimSpace(job.ID) == "" {
		return ""
	}
	return fmt.Sprintf("status:%s:%d:%d:%s", job.Repo, job.IssueNumber, job.TriggerCommentID, job.ID)
}

func (s *Service) now() time.Time {
	if s.Clock != nil {
		return s.Clock().UTC()
	}
	return time.Now().UTC()
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len([]byte(msg)) <= 1024 {
		return msg
	}
	for len([]byte(msg)) > 1021 {
		msg = msg[:len(msg)-1]
	}
	return msg + "..."
}
