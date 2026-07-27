// Package artifacts maintains issue-spec marker projections without ever
// rewriting the raw issue or comment body that produced them.
package artifacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// MarkerProjector is strict for typed-comment conflicts. Synchronous comment
// mutations already run in one database transaction, so a duplicate typed ID
// rolls back both the raw comment and every projection-side effect.
type MarkerProjector struct{}

// TolerantMarkerProjector is reserved for projection rebuilds over raw comments
// that already exist. It records duplicate typed IDs as anomalies instead of
// requiring the caller to roll back the source comment.
type TolerantMarkerProjector struct{ MarkerProjector }

type TypedCommentConflictError struct {
	ID          string
	SuggestedID string
}

func (e *TypedCommentConflictError) Error() string {
	if e.SuggestedID != "" {
		return fmt.Sprintf("typed comment ID %s already exists; suggested ID %s", e.ID, e.SuggestedID)
	}
	return fmt.Sprintf("typed comment ID %s already exists", e.ID)
}

func (e *TypedCommentConflictError) Unwrap() error { return store.ErrProjectionConflict }

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
	return projectCommentAndIncrement(ctx, repository, snapshot, true)
}

func (TolerantMarkerProjector) ProjectComment(ctx context.Context, repository store.RepoStore,
	snapshot models.CommentSnapshot) error {
	return projectCommentAndIncrement(ctx, repository, snapshot, false)
}

func projectCommentAndIncrement(ctx context.Context, repository store.RepoStore,
	snapshot models.CommentSnapshot, rejectTypedCommentConflicts bool) error {
	if err := projectComment(ctx, repository, snapshot, rejectTypedCommentConflicts); err != nil {
		return err
	}
	_, err := repository.IncrementCollectionVersions(ctx, store.RepoCollectionArtifacts)
	return err
}

func projectComment(ctx context.Context, repository store.RepoStore, snapshot models.CommentSnapshot,
	rejectTypedCommentConflicts bool) error {
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
		"links": parsed.Links, "marker_version": parsed.Marker.Version,
	})
	err := repository.ApplyTypedCommentProjection(ctx, store.TypedCommentProjectionInput{
		IssueID: comment.IssueID, CommentID: comment.ID, Type: parsed.Type,
		Key: parsed.ID, Body: comment.Body, Metadata: metadata, UserID: comment.AuthorID,
	})
	if errors.Is(err, store.ErrProjectionConflict) {
		if rejectTypedCommentConflicts {
			suggestedID, _ := suggestTypedCommentID(ctx, repository, snapshot.IssueNumber, parsed.Type)
			return &TypedCommentConflictError{ID: parsed.ID, SuggestedID: suggestedID}
		}
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

func suggestTypedCommentID(ctx context.Context, repository store.RepoStore, issueNumber int64,
	commentType string) (string, error) {
	const pageSize = 100
	var ids []string
	for pageNumber := 1; ; pageNumber++ {
		page, err := repository.ListComments(ctx, models.CommentListOptions{
			IssueNumber: &issueNumber, Page: pageNumber, PerPage: pageSize,
		})
		if err != nil {
			return "", err
		}
		for _, item := range page.Items {
			parsed := model.ParseTypedComment(item.Comment.Body)
			if parsed.Type == commentType {
				ids = append(ids, parsed.ID)
			}
		}
		if pageNumber*pageSize >= page.Total {
			break
		}
	}
	return nextIssueScopedTypedCommentID(commentType, issueNumber, ids), nil
}

func nextIssueScopedTypedCommentID(commentType string, issueNumber int64, ids []string) string {
	prefix := commentType + "-" + strconv.FormatInt(issueNumber, 10)
	maxSequence := 0
	for _, id := range ids {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		sequenceText := strings.TrimPrefix(id, prefix)
		if len(sequenceText) != 3 {
			continue
		}
		sequence, err := strconv.Atoi(sequenceText)
		if err == nil && sequence > maxSequence {
			maxSequence = sequence
		}
	}
	if maxSequence >= 999 {
		return ""
	}
	return fmt.Sprintf("%s-%d%03d", commentType, issueNumber, maxSequence+1)
}
