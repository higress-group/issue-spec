package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

type CandidateKind string

const (
	CandidateJob    CandidateKind = "job"
	CandidateCancel CandidateKind = "cancel"
)

type Candidate struct {
	Kind           CandidateKind
	Repo           string
	IssueNumber    int
	CommentID      int64
	CommentURL     string
	Command        commentrunner.Command
	IdempotencyKey string
}

type Result struct {
	Candidates   []Candidate
	UpdatedAt    time.Time
	PollInterval string
}

type Runner struct {
	Backend   github.RunnerOperations
	Store     *state.Store
	Allowlist commentrunner.Allowlist
	Perms     commentrunner.PermissionLookup
	Now       func() time.Time
}

func (r Runner) Poll(ctx context.Context, repo string) (Result, error) {
	now := r.now()
	var out Result
	err := r.Store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, repo)
		result, err := r.pollRepo(ctx, repo, rs, now)
		if err != nil {
			return err
		}
		if result.UpdatedAt.IsZero() {
			result.UpdatedAt = now
		}
		out = result
		if result.PollInterval != "" {
			st.Polling.PollInterval = result.PollInterval
		}
		return nil
	})
	return out, err
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r Runner) pollRepo(ctx context.Context, repo string, rs *model.RepoState, now time.Time) (Result, error) {
	var out Result
	notes, meta, err := r.Backend.ListNotifications(ctx, github.NotificationListOptions{ETag: rs.Validators.NotificationsETag})
	if err != nil {
		return Result{}, err
	}
	if meta.ETag != "" {
		rs.Validators.NotificationsETag = meta.ETag
	}
	if meta.PollInterval != "" {
		rs.Validators.UpdatedAt = now
	}
	if meta.NotModified {
		return Result{}, nil
	}
	out.PollInterval = meta.PollInterval
	for _, note := range notes {
		threadURL := strings.TrimSpace(note.Subject.URL)
		if threadURL == "" {
			continue
		}
		comments, cmeta, err := r.Backend.GetNotificationComments(ctx, threadURL, github.NotificationCommentOptions{ETag: rs.Validators.CommentsETag})
		if err != nil {
			return Result{}, err
		}
		if cmeta.ETag != "" {
			rs.Validators.CommentsETag = cmeta.ETag
		}
		if cmeta.NotModified {
			continue
		}
		for _, comment := range comments {
			cand, ok, err := r.commentCandidate(ctx, repo, comment, now, rs)
			if err != nil || !ok {
				if err != nil {
					return Result{}, err
				}
				continue
			}
			out.Candidates = append(out.Candidates, cand)
		}
	}
	// Lower cadence fallback for repository comments to catch missed/self-authored comments.
	repoComments, _, err := r.Backend.ListRepositoryIssueComments(ctx, repo, github.RepositoryIssueCommentOptions{})
	if err != nil {
		return Result{}, err
	}
	for _, rc := range repoComments {
		cand, ok, err := r.commentCandidate(ctx, repo, rc.Comment, now, rs)
		if err != nil || !ok {
			if err != nil {
				return Result{}, err
			}
			continue
		}
		if rc.IssueNumber > 0 {
			cand.IssueNumber = rc.IssueNumber
		}
		out.Candidates = append(out.Candidates, cand)
	}
	return out, nil
}

func (r Runner) commentCandidate(ctx context.Context, repo string, c github.Comment, now time.Time, rs *model.RepoState) (Candidate, bool, error) {
	if _, seen := rs.FirstObservedComments[fmt.Sprint(c.ID)]; seen {
		return Candidate{}, false, nil
	}
	rs.FirstObservedComments[fmt.Sprint(c.ID)] = model.ObservedComment{CommentID: c.ID, ObservedAt: now}
	cmd, err := commentrunner.ParseCommand(c.Body)
	if err != nil {
		return Candidate{}, false, nil
	}
	if err := commentrunner.Authorized(ctx, repo, loginFromComment(c), cmd, r.Allowlist, r.Perms); err != nil {
		return Candidate{}, false, nil
	}
	kind := CandidateJob
	if cmd.Kind == commentrunner.KindCancel {
		kind = CandidateCancel
	}
	key := idempotencyKey(repo, c.ID, kind, cmd)
	if rec, ok := state.LookupIdempotency(rs, key); ok && rec.ResourceID != "" {
		return Candidate{}, false, nil
	}
	if rs.Idempotency == nil {
		rs.Idempotency = map[string]model.IdempotencyRecord{}
	}
	rs.Idempotency[key] = model.IdempotencyRecord{Key: key, Kind: string(kind), ResourceID: fmt.Sprint(c.ID), CreatedAt: now}
	issueNumber := 0
	if c.HTMLURL != "" {
		if n, err := github.ParseIssueNumber(c.HTMLURL); err == nil {
			issueNumber = n
		}
	}
	return Candidate{Kind: kind, Repo: repo, IssueNumber: issueNumber, CommentID: c.ID, CommentURL: c.HTMLURL, Command: cmd, IdempotencyKey: key}, true, nil
}

func loginFromComment(c github.Comment) string {
	if c.User == nil {
		return ""
	}
	return strings.TrimSpace(c.User.Login)
}

func idempotencyKey(repo string, commentID int64, kind CandidateKind, cmd commentrunner.Command) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{repo, fmt.Sprint(commentID), string(kind), string(cmd.Kind), cmd.PublicSession, cmd.Prompt}, "\x00")))
	return "intake:" + hex.EncodeToString(sum[:])
}
