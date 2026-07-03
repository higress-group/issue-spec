package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/workspace"
)

type Store interface {
	Update(func(*model.RunnerState) error) error
}

type WorkspaceManager interface {
	PrepareNew(spec workspace.WorkspaceSpec) (*model.WorkspaceState, error)
	Resume(model.WorkspaceState) error
	Lock(path string) (func() error, error)
}

type AcpxRunner interface {
	Run(context.Context, acpx.SessionRequest) (acpx.SessionResult, error)
}

type TurnCanceller interface {
	CancelTurn(context.Context, string) error
}

type WritebackBackend interface {
	writeback.Backend
}

type Dispatcher struct {
	Store     Store
	Workspace WorkspaceManager
	Acpx      AcpxRunner
	Canceller TurnCanceller
	Backend   WritebackBackend
	Now       func() time.Time
}

type DispatchRequest struct {
	Repo            string
	RepoKey         string
	IssueNumber     int
	IssueKey        string
	Commenter       string
	Command         commentrunner.Command
	Prompt          string
	RepoURL         string
	DefaultBranch   string
	TrustedRef      string
	WorkspaceID     string
	JobID           string
	PublicSessionID string
}

func (d Dispatcher) RunNew(ctx context.Context, req DispatchRequest) (*model.JobState, error) {
	return d.run(ctx, req, true)
}

func (d Dispatcher) RunResume(ctx context.Context, req DispatchRequest) (*model.JobState, error) {
	return d.run(ctx, req, false)
}

func (d Dispatcher) ReconcileStartup(ctx context.Context, repoKey string) error {
	if d.Store == nil || d.Backend == nil {
		return fmt.Errorf("dispatcher dependencies are required")
	}
	now := d.now()
	return d.Store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, repoKey)
		for _, job := range rs.Jobs {
			switch job.Status {
			case "dispatched", "running":
				if job.AcpxID != "" {
					job.Status = "running"
				}
				job.UpdatedAt = now
			case "ambiguous":
				job.Status = "interrupted"
				job.UpdatedAt = now
			}
		}
		return nil
	})
}

func (d Dispatcher) run(ctx context.Context, req DispatchRequest, isNew bool) (*model.JobState, error) {
	if d.Store == nil || d.Workspace == nil || d.Acpx == nil || d.Backend == nil {
		return nil, fmt.Errorf("dispatcher dependencies are required")
	}
	now := d.now()
	var out *model.JobState
	var unlock func() error
	var ws *model.WorkspaceState
	var created bool
	err := d.Store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, req.RepoKey)
		if rs.Jobs == nil {
			rs.Jobs = map[string]*model.JobState{}
		}
		if rs.PublicSessions == nil {
			rs.PublicSessions = map[string]*model.PublicSessionState{}
		}
		if rs.Workspaces == nil {
			rs.Workspaces = map[string]*model.WorkspaceState{}
		}
		job, isCreated, err := d.initJob(rs, req, now, isNew)
		if err != nil {
			return err
		}
		out = job
		created = isCreated
		if !created {
			return nil
		}
		if isNew {
			ws = &model.WorkspaceState{ID: job.WorkspaceID, Repo: req.RepoKey, UpdatedAt: now}
			rs.Workspaces[job.WorkspaceID] = ws
		} else if existing := rs.Workspaces[job.WorkspaceID]; existing != nil {
			ws = existing
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return out, nil
	}
	if ws == nil {
		return nil, fmt.Errorf("workspace state missing")
	}
	var lockErr error
	if isNew {
		created, err := d.Workspace.PrepareNew(workspace.WorkspaceSpec{
			RepoURL:       req.RepoURL,
			RepoKey:       req.RepoKey,
			WorkspaceID:   out.WorkspaceID,
			DefaultBranch: req.DefaultBranch,
			TrustedRef:    req.TrustedRef,
		})
		if err != nil {
			return d.fail(ctx, req, out, err, "failed")
		}
		ws = created
		_ = d.Store.Update(func(st *model.RunnerState) error {
			rs := state.EnsureRepo(st, req.RepoKey)
			rs.Workspaces[out.WorkspaceID] = ws
			return nil
		})
	} else if ws.Path == "" {
		return d.fail(ctx, req, out, fmt.Errorf("workspace state missing path"), "failed")
	}
	if unlock, lockErr = d.Workspace.Lock(ws.Path); lockErr != nil {
		return d.fail(ctx, req, out, lockErr, "rejected")
	}
	defer func() {
		if unlock != nil {
			_ = unlock()
		}
	}()
	if !isNew {
		if err := d.Workspace.Resume(*ws); err != nil {
			return d.fail(ctx, req, out, err, "failed")
		}
	}
	if err := d.writeStatus(ctx, req, out, "running", nil); err != nil {
		return d.fail(ctx, req, out, err, "failed")
	}
	res, err := d.Acpx.Run(ctx, acpx.SessionRequest{Mode: mode(isNew), SessionID: req.PublicSessionID, Prompt: req.Prompt, Queue: true, RefreshMeta: true})
	if err != nil {
		return d.fail(ctx, req, out, err, "failed")
	}
	_ = d.Store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, req.RepoKey)
		if job := rs.Jobs[out.ID]; job != nil {
			job.Status = terminalState(res)
			job.AcpxID = res.SessionID
			job.UpdatedAt = now
		}
		if sess := rs.PublicSessions[req.PublicSessionID]; sess != nil {
			sess.ProviderID = res.SessionID
			sess.UpdatedAt = now
		}
		if ac := rs.Acpx[out.ID]; ac != nil {
			ac.SessionID = res.SessionID
			ac.Metadata = res.Metadata
		}
		return nil
	})
	_ = d.writeStatus(ctx, req, out, terminalState(res), nil)
	return out, nil
}

func (d Dispatcher) initJob(rs *model.RepoState, req DispatchRequest, now time.Time, isNew bool) (*model.JobState, bool, error) {
	if req.JobID == "" {
		req.JobID = newID("job")
	}
	if req.PublicSessionID == "" {
		req.PublicSessionID = newID("sess")
	}
	if req.WorkspaceID == "" {
		req.WorkspaceID = req.PublicSessionID
	}
	if existing := rs.Jobs[req.JobID]; existing != nil {
		if existing.PublicSessionID != req.PublicSessionID {
			return nil, false, fmt.Errorf("duplicate job id")
		}
		return existing, false, nil
	}
	job := &model.JobState{ID: req.JobID, Command: req.Prompt, Status: "running", PublicSessionID: req.PublicSessionID, WorkspaceID: req.WorkspaceID, CreatedAt: now, UpdatedAt: now}
	rs.Jobs[job.ID] = job
	rs.PublicSessions[req.PublicSessionID] = &model.PublicSessionState{RepoID: req.RepoKey, PublicSessionID: req.PublicSessionID, JobID: job.ID, CreatedAt: now, UpdatedAt: now}
	rs.Acpx[job.ID] = &model.AcpxState{ID: job.ID, Metadata: map[string]string{}}
	rs.Idempotency[dispatchKey(req, isNew)] = model.IdempotencyRecord{Key: dispatchKey(req, isNew), Kind: "job", ResourceID: job.ID, CreatedAt: now}
	return job, true, nil
}

func (d Dispatcher) Cancel(ctx context.Context, req DispatchRequest) (*model.JobState, error) {
	if d.Store == nil || d.Backend == nil || d.Canceller == nil {
		return nil, fmt.Errorf("dispatcher dependencies are required")
	}
	now := d.now()
	var out *model.JobState
	var repoKey string
	err := d.Store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, req.RepoKey)
		repoKey = req.RepoKey
		job := jobForCancellation(rs, req.PublicSessionID)
		if job == nil {
			return fmt.Errorf("cancel target not found")
		}
		out = job
		if err := d.Canceller.CancelTurn(ctx, job.PublicSessionID); err != nil {
			return err
		}
		job.Status = "cancelled"
		job.UpdatedAt = now
		if rs.Cancellation == nil {
			rs.Cancellation = map[string]model.CancellationState{}
		}
		rs.Cancellation[job.PublicSessionID] = model.CancellationState{Key: job.PublicSessionID, JobID: job.ID, CancelledAt: now}
		return nil
	})
	if err != nil {
		return nil, err
	}
	_ = d.writeStatus(ctx, DispatchRequest{Repo: req.Repo, RepoKey: repoKey, IssueNumber: req.IssueNumber, IssueKey: req.IssueKey, Prompt: req.Prompt}, out, "cancelled", nil)
	return out, nil
}

func jobForCancellation(rs *model.RepoState, publicSessionID string) *model.JobState {
	if rs == nil {
		return nil
	}
	for _, job := range rs.Jobs {
		if job != nil && job.PublicSessionID == publicSessionID {
			return job
		}
	}
	return nil
}

func (d Dispatcher) fail(ctx context.Context, req DispatchRequest, job *model.JobState, err error, terminal string) (*model.JobState, error) {
	if job != nil {
		_ = d.Store.Update(func(st *model.RunnerState) error {
			rs := state.EnsureRepo(st, req.RepoKey)
			if rs.Jobs != nil && rs.Jobs[job.ID] != nil {
				rs.Jobs[job.ID].Status = terminal
			}
			return nil
		})
		_ = d.writeStatus(ctx, req, job, terminal, []string{err.Error()})
	}
	return nil, err
}

func (d Dispatcher) writeStatus(ctx context.Context, req DispatchRequest, job *model.JobState, terminal string, diag []string) error {
	return d.Store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, req.RepoKey)
		_, err := writeback.UpsertStatusComment(ctx, d.Backend, rs, writeback.Request{
			Repo:        req.Repo,
			IssueNumber: req.IssueNumber,
			IssueKey:    req.IssueKey,
			JobID:       job.ID,
			State:       terminal,
			Command:     req.Prompt,
			Diagnostics: diag,
		})
		return err
	})
}

func (d Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

func mode(isNew bool) acpx.Mode {
	if isNew {
		return acpx.ModeFresh
	}
	return acpx.ModeResume
}

func terminalState(res acpx.SessionResult) string {
	if res.Queued {
		return "queued"
	}
	return "done"
}

func dispatchKey(req DispatchRequest, isNew bool) string {
	return strings.Join([]string{req.RepoKey, req.PublicSessionID, req.JobID, fmt.Sprint(isNew)}, ":")
}
