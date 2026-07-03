package writeback

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

type Backend interface {
	ListIssueComments(context.Context, string, int) ([]github.Comment, error)
	CreateComment(context.Context, string, int, string) (github.Comment, error)
	UpdateComment(context.Context, string, int64, string) (github.Comment, error)
}

type Request struct {
	Repo        string
	IssueNumber int
	IssueKey    string
	JobID       string
	Agent       string
	State       string
	Command     string
	Provenance  string
	Diagnostics []string
	Interrupted bool
	Cancelled   bool
}

var statusMarkerRe = regexp.MustCompile(`(?s)<!--\s*issue-spec:status\s+([^>]*)-->`)

type StatusMarker struct {
	JobID   string
	Version int
}

func UpsertStatusComment(ctx context.Context, backend Backend, rs *model.RepoState, req Request) (github.Comment, error) {
	if backend == nil {
		return github.Comment{}, fmt.Errorf("backend is required")
	}
	if rs == nil {
		return github.Comment{}, fmt.Errorf("repo state is required")
	}
	body, err := templates.StatusComment(templates.StatusCommentOptions{
		JobID:       req.JobID,
		Agent:       req.Agent,
		State:       req.State,
		Command:     req.Command,
		Provenance:  req.Provenance,
		Diagnostics: req.Diagnostics,
		Interrupted: req.Interrupted,
		Cancelled:   req.Cancelled,
	})
	if err != nil {
		return github.Comment{}, err
	}
	if rs.StatusComments == nil {
		rs.StatusComments = map[string]*model.StatusCommentState{}
	}
	key := strings.TrimSpace(req.IssueKey)
	if key == "" {
		key = fmt.Sprintf("%d", req.IssueNumber)
	}
	if state := rs.StatusComments[key]; state != nil && state.CommentID != 0 {
		return backend.UpdateComment(ctx, req.Repo, state.CommentID, body)
	}
	comments, err := backend.ListIssueComments(ctx, req.Repo, req.IssueNumber)
	if err != nil {
		return github.Comment{}, err
	}
	for _, c := range comments {
		if marker, ok := ParseStatusMarker(c.Body); ok && marker.JobID == req.JobID {
			rs.StatusComments[key] = &model.StatusCommentState{IssueID: key, CommentID: c.ID, UpdatedAt: time.Now()}
			return backend.UpdateComment(ctx, req.Repo, c.ID, body)
		}
	}
	created, err := backend.CreateComment(ctx, req.Repo, req.IssueNumber, body)
	if err != nil {
		return github.Comment{}, err
	}
	rs.StatusComments[key] = &model.StatusCommentState{IssueID: key, CommentID: created.ID, UpdatedAt: time.Now()}
	return created, nil
}

func ParseStatusMarker(body string) (StatusMarker, bool) {
	matches := statusMarkerRe.FindAllStringSubmatch(body, -1)
	for _, match := range matches {
		attrs := parseMarkerAttrs(match[1])
		if len(attrs) != 2 {
			continue
		}
		jobID := strings.TrimSpace(attrs["job"])
		if jobID == "" {
			continue
		}
		version := 1
		if raw := strings.TrimSpace(attrs["version"]); raw != "" && raw != "1" {
			continue
		}
		return StatusMarker{JobID: jobID, Version: version}, true
	}
	return StatusMarker{}, false
}

func parseMarkerAttrs(raw string) map[string]string {
	out := map[string]string{}
	for _, field := range strings.Fields(raw) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out
}
