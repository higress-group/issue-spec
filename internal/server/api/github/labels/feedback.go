package labels

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/feedback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

// PromoteFeedback uses the same repository permission as creating an Issue.
// The published policy and persisted occurrence evidence determine the label.
func (s *Service) PromoteFeedback(ctx context.Context, owner, repository string, number int64, expectedPolicy string, subject authz.Subject) (feedback.Promotion, error) {
	result := feedback.Promotion{Status: "pending", Reason: "feedback policy changed or could not be rechecked"}
	var resource models.RepositoryResource
	err := s.mutate(ctx, owner, repository, subject, authz.OperationContribute, func(repositoryStore store.RepoStore, actor uuid.UUID) error {
		definition, err := repositoryStore.LabelByNameForShare(ctx, feedback.PolicyLabel)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if definition.Description != expectedPolicy {
			return nil
		}
		policy, policyIssue, err := feedback.ResolvePublishedPolicy(definition.Description, func(number int) (github.Issue, error) {
			issue, err := repositoryStore.IssueByNumber(ctx, int64(number))
			return github.Issue{Body: issue.Body}, err
		})
		if err != nil {
			result.Reason = err.Error()
			return nil
		}
		if int64(policyIssue) == number {
			return nil
		}

		// Lock the target definition before the Issue, in the same order as label
		// definition updates that invalidate associated Issue representations.
		_, labelErr := repositoryStore.LabelByNameForShare(ctx, policy.PriorityLabel)
		if labelErr != nil && !errors.Is(labelErr, store.ErrNotFound) {
			return labelErr
		}
		issue, labels, err := repositoryStore.IssueLabelsForUpdate(ctx, number)
		if err != nil {
			return err
		}
		input := github.Issue{State: string(issue.State), Body: issue.Body, User: feedbackAuthor(issue.AuthorID)}
		for _, label := range labels {
			input.Labels = append(input.Labels, github.Label{Name: label.Name})
		}
		records, err := repositoryStore.ListIssueCommentsWithPrefix(ctx, issue.ID, feedback.OccurrencePrefix)
		if err != nil {
			return err
		}
		comments := make([]github.Comment, 0, len(records))
		for _, record := range records {
			comments = append(comments, github.Comment{Body: record.Body, User: feedbackAuthor(record.AuthorID)})
		}
		result = feedback.EvaluatePromotion(input, comments, policy)
		if result.Status != "would_promote" {
			return nil
		}
		if labelErr != nil {
			result.Status = "pending"
			result.Reason = "configured priority label unavailable"
			return nil
		}
		if _, _, err := repositoryStore.AddIssueLabels(ctx, number, []string{policy.PriorityLabel}, actor); err != nil {
			return err
		}
		result.Status = "applied"
		return nil
	}, &resource)
	return result, err
}

func feedbackAuthor(id *uuid.UUID) *github.User {
	if id == nil {
		return nil
	}
	return &github.User{ID: codec.StableNumericID(id.String())}
}
