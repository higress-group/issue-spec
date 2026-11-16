package feedback

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
)

type Result struct {
	Repository     string    `json:"repository"`
	Issue          int       `json:"issue"`
	URL            string    `json:"url"`
	EventKey       string    `json:"event_key"`
	EventStatus    string    `json:"event_status"`
	CommentID      int64     `json:"comment_id,omitempty"`
	Confirmed      bool      `json:"confirmed"`
	ConfirmedCount int       `json:"confirmed_count"`
	CountStale     bool      `json:"count_stale,omitempty"`
	DryRun         bool      `json:"dry_run"`
	Promotion      Promotion `json:"promotion"`
}

type Promotion struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type occurrence struct {
	found     bool
	confirmed bool
	commentID int64
}

type snapshot struct {
	issue     github.Issue
	events    map[string]occurrence
	confirmed int
}

func Record(ctx context.Context, backend Backend, repo string, number int, event Event, dryRun bool) (Result, error) {
	policy, policyIssue, policyRef, err := loadPolicy(ctx, backend, repo)
	if err != nil {
		return Result{}, err
	}
	if policyIssue == number {
		return Result{}, fmt.Errorf("choose a feedback Issue distinct from the policy Issue")
	}
	actor, _, err := backend.GetUser(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("resolve feedback reporter: %w", err)
	}
	key := event.Key()
	trusted := policy.trusts(&actor)
	observed, err := observe(ctx, backend, repo, number, policy)
	if err != nil {
		return Result{}, err
	}
	result := Result{Repository: repo, Issue: number, URL: observed.issue.HTMLURL, EventKey: key, DryRun: dryRun}
	recorded := observed.events[key]
	needsRecord := !recorded.found || (trusted && !recorded.confirmed)
	result.EventStatus = "already_recorded"
	if dryRun && needsRecord {
		result.EventStatus = "would_record"
		recorded.confirmed = trusted
		if trusted {
			observed.confirmed++
		}
	}
	if !dryRun && needsRecord {
		body, err := event.commentBody()
		if err != nil {
			return Result{}, err
		}
		comment, createErr := backend.CreateComment(ctx, repo, number, body)
		refreshed, readErr := observe(ctx, backend, repo, number, policy)
		current := refreshed.events[key]
		reconciled := readErr == nil && current.found && (!trusted || current.confirmed)
		if createErr != nil && !reconciled {
			return Result{}, fmt.Errorf("record feedback occurrence %s: %w", key, createErr)
		}
		if !reconciled {
			observed.addEvent(event, policy.trusts(comment.User), comment.ID)
			result.EventStatus = "recorded"
			result.CountStale = true
			result.setOccurrence(observed.events[key], observed.confirmed)
			result.Promotion = Promotion{Label: policy.PriorityLabel, Status: "pending", Reason: "evidence saved; retry to refresh confirmed evidence and Issue state"}
			return result, nil
		}
		if createErr == nil {
			result.EventStatus = "recorded"
		}
		observed, recorded = refreshed, current
	}
	result.setOccurrence(recorded, observed.confirmed)
	result.Promotion = promotionPlan(observed.issue, policy, observed.confirmed)
	if result.Promotion.Status != "would_promote" || dryRun {
		return result, nil
	}
	result.Promotion = applyPromotion(ctx, backend, repo, number, policy, policyRef, observed.confirmed)
	return result, nil
}

func observe(ctx context.Context, backend github.IssueBackend, repo string, number int, policy Policy) (snapshot, error) {
	issue, err := backend.GetIssue(ctx, repo, number)
	if err != nil {
		return snapshot{}, fmt.Errorf("read feedback Issue #%d: %w", number, err)
	}
	if issue.PullRequest != nil {
		return snapshot{}, fmt.Errorf("feedback must refer to an Issue")
	}
	comments, err := backend.ListIssueComments(ctx, repo, number)
	if err != nil {
		return snapshot{}, fmt.Errorf("read feedback Issue #%d comments: %w", number, err)
	}
	state := snapshot{issue: issue, events: make(map[string]occurrence)}
	state.addBody(issue.Body, policy.trusts(issue.User), 0)
	for _, comment := range comments {
		state.addBody(comment.Body, policy.trusts(comment.User), comment.ID)
	}
	return state, nil
}

func (s *snapshot) addBody(body string, trusted bool, commentID int64) {
	appendEvents(body, func(event Event) {
		s.addEvent(event, trusted, commentID)
	})
}

func (s *snapshot) addEvent(event Event, confirmed bool, commentID int64) {
	key := event.Key()
	previous := s.events[key]
	if confirmed && !previous.confirmed {
		s.confirmed++
	}
	if !previous.found || (confirmed && !previous.confirmed) {
		s.events[key] = occurrence{found: true, confirmed: confirmed, commentID: commentID}
	}
}

func (r *Result) setOccurrence(recorded occurrence, count int) {
	r.Confirmed = recorded.confirmed
	r.CommentID = recorded.commentID
	r.ConfirmedCount = count
}

func promotionPlan(issue github.Issue, policy Policy, count int) Promotion {
	plan := Promotion{Label: policy.PriorityLabel}
	switch {
	case issue.State != "open":
		plan.Status = "closed"
	case issue.Locked || hasLabel(issue.Labels, holdLabel) || hasLabel(issue.Labels, "wontfix") || hasAnyLabel(issue.Labels, policy.HoldLabels):
		plan.Status = "held"
	case hasLabel(issue.Labels, policy.PriorityLabel) || hasAnyLabel(issue.Labels, policy.HigherPriorityLabels):
		plan.Status = "already_at_priority"
	case count < policy.Threshold:
		plan.Status = "below_threshold"
	case policy.Mode == modeSuggest:
		plan.Status = "suggested"
	default:
		plan.Status = "would_promote"
	}
	return plan
}

func applyPromotion(ctx context.Context, backend Backend, repo string, number int, policy Policy, policyRef string, count int) Promotion {
	pending := Promotion{Label: policy.PriorityLabel, Status: "pending"}
	label, err := backend.GetRepositoryLabel(ctx, repo, policyLabel)
	if err != nil || label.Description != policyRef {
		pending.Reason = "feedback policy changed or could not be rechecked"
		return pending
	}
	issue, err := backend.GetIssue(ctx, repo, number)
	if err != nil {
		pending.Reason = "could not recheck Issue state and labels"
		return pending
	}
	plan := promotionPlan(issue, policy, count)
	if plan.Status != "would_promote" {
		return plan
	}
	if _, err := backend.GetRepositoryLabel(ctx, repo, policy.PriorityLabel); err != nil {
		pending.Reason = fmt.Sprintf("configured priority label unavailable: %v", err)
		return pending
	}
	labels, err := backend.AddIssueLabels(ctx, repo, number, []string{policy.PriorityLabel})
	if err != nil {
		// The provider may have accepted an addition whose response was lost.
		if current, readErr := backend.GetIssue(ctx, repo, number); readErr == nil && hasLabel(current.Labels, policy.PriorityLabel) {
			return Promotion{Label: policy.PriorityLabel, Status: "applied"}
		}
		pending.Reason = err.Error()
		return pending
	}
	if !hasLabel(labels, policy.PriorityLabel) {
		pending.Reason = "the backend did not assign the label; check label existence and triage permission"
		return pending
	}
	return Promotion{Label: policy.PriorityLabel, Status: "applied"}
}

func hasLabel(labels []github.Label, name string) bool {
	return slices.ContainsFunc(labels, func(label github.Label) bool {
		return strings.EqualFold(label.Name, name)
	})
}

func hasAnyLabel(labels []github.Label, names []string) bool {
	return slices.ContainsFunc(names, func(name string) bool {
		return hasLabel(labels, name)
	})
}
