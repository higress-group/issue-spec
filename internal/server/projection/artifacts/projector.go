// Package artifacts maintains issue-spec marker projections without ever
// rewriting the raw issue or comment body that produced them.
package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

var issueMarker = regexp.MustCompile(`(?s)<!--\s*issue-spec:issue=([^\s>]+)\s+change=([^\s>]+)\s+version=([^\s>]+)\s*-->`)

type Projector interface {
	ProjectIssue(context.Context, store.RepoStore, models.Issue) error
	ProjectComment(context.Context, store.RepoStore, models.CommentSnapshot) error
}

type MarkerProjector struct{}

func (MarkerProjector) ProjectIssue(ctx context.Context, repository store.RepoStore, issue models.Issue) error {
	if err := projectIssue(ctx, repository, issue); err != nil {
		return err
	}
	_, err := repository.IncrementCollectionVersions(ctx, store.RepoCollectionArtifacts)
	return err
}

func projectIssue(ctx context.Context, repository store.RepoStore, issue models.Issue) error {
	matches := issueMarker.FindStringSubmatch(issue.Body)
	if len(matches) == 0 {
		if err := repository.ClearIssueProjection(ctx, issue.ID); err != nil {
			return err
		}
		if strings.Contains(issue.Body, "issue-spec:issue=") {
			return repository.RecordProjectionAnomaly(ctx, store.ProjectionAnomalyInput{
				SourceType: "issue", SourceID: issue.ID, Key: "malformed_issue_marker",
				Details: json.RawMessage(`{"reason":"marker is malformed"}`),
			})
		}
		return repository.ResolveProjectionAnomalies(ctx, "issue", issue.ID)
	}
	kind, changeKey := strings.ToLower(matches[1]), matches[2]
	version, versionErr := strconv.Atoi(matches[3])
	if versionErr != nil || version != 1 || (kind != "proposal" && kind != "design" && kind != "implement") || strings.TrimSpace(changeKey) == "" {
		if err := repository.ClearIssueProjection(ctx, issue.ID); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"kind": kind, "version": matches[3], "reason": "unsupported issue marker"})
		return repository.RecordProjectionAnomaly(ctx, store.ProjectionAnomalyInput{
			SourceType: "issue", SourceID: issue.ID, Key: "unsupported_issue_marker", Details: details,
		})
	}
	metadata, _ := json.Marshal(map[string]any{"marker_version": version, "source": "issue"})
	if err := repository.ApplyIssueProjection(ctx, store.IssueProjectionInput{
		IssueID: issue.ID, ChangeKey: changeKey, ArtifactType: kind,
		Content: issue.Body, Metadata: metadata, UserID: issue.AuthorID,
	}); errors.Is(err, store.ErrProjectionConflict) {
		if clearErr := repository.ClearIssueProjection(ctx, issue.ID); clearErr != nil {
			return clearErr
		}
		return repository.RecordProjectionAnomaly(ctx, store.ProjectionAnomalyInput{
			SourceType: "issue", SourceID: issue.ID, Key: "duplicate_issue_artifact",
			Details: json.RawMessage(`{"reason":"change artifact already belongs to another issue"}`),
		})
	} else if err != nil {
		return err
	}
	return repository.ResolveProjectionAnomalies(ctx, "issue", issue.ID)
}

func (MarkerProjector) ProjectComment(ctx context.Context, repository store.RepoStore, snapshot models.CommentSnapshot) error {
	if err := projectComment(ctx, repository, snapshot); err != nil {
		return err
	}
	_, err := repository.IncrementCollectionVersions(ctx, store.RepoCollectionArtifacts)
	return err
}

func projectComment(ctx context.Context, repository store.RepoStore, snapshot models.CommentSnapshot) error {
	comment := snapshot.Comment
	parsed := model.ParseTypedComment(comment.Body)
	if parsed.Marker.Type == "" && parsed.Marker.ID == "" && len(parsed.Errors) == 0 {
		if err := repository.ClearTypedCommentProjection(ctx, comment.ID); err != nil {
			return err
		}
		return repository.ResolveProjectionAnomalies(ctx, "comment", comment.ID)
	}
	if parsed.Marker.Version != 1 || len(parsed.Errors) > 0 {
		if err := repository.ClearTypedCommentProjection(ctx, comment.ID); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"errors": parsed.Errors, "version": parsed.Marker.Version})
		return repository.RecordProjectionAnomaly(ctx, store.ProjectionAnomalyInput{
			SourceType: "comment", SourceID: comment.ID, Key: "invalid_typed_comment", Details: details,
		})
	}
	metadata, _ := json.Marshal(map[string]any{
		"status": parsed.Status, "scope": parsed.Scope, "agent": parsed.Agent,
		"agent_session_id": parsed.AgentSessionID, "links": parsed.Links, "marker_version": parsed.Marker.Version,
	})
	err := repository.ApplyTypedCommentProjection(ctx, store.TypedCommentProjectionInput{
		IssueID: comment.IssueID, CommentID: comment.ID, Type: parsed.Type,
		Key: parsed.ID, Body: comment.Body, Metadata: metadata, UserID: comment.AuthorID,
	})
	if errors.Is(err, store.ErrProjectionConflict) {
		if clearErr := repository.ClearTypedCommentProjection(ctx, comment.ID); clearErr != nil {
			return clearErr
		}
		return repository.RecordProjectionAnomaly(ctx, store.ProjectionAnomalyInput{
			SourceType: "comment", SourceID: comment.ID, Key: "duplicate_typed_comment_key",
			Details: json.RawMessage(`{"reason":"typed comment key already exists"}`),
		})
	}
	if err != nil {
		return err
	}
	return repository.ResolveProjectionAnomalies(ctx, "comment", comment.ID)
}
