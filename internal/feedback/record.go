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

type Promotion = github.FeedbackPromotion

type RecordOptions struct {
	DryRun     bool
	SelfHosted bool
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

func Record(ctx context.Context, backend Backend, repo string, number int, event Event, options RecordOptions) (Result, error) {
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
	result := Result{Repository: repo, Issue: number, URL: observed.issue.HTMLURL, EventKey: key, DryRun: options.DryRun}
	recorded := observed.events[key]
	needsRecord := !recorded.found || (trusted && !recorded.confirmed)
	result.EventStatus = "already_recorded"
	if options.DryRun && needsRecord {
		result.EventStatus = "would_record"
		recorded.confirmed = trusted
		if trusted {
			observed.confirmed++
		}
	}
	if !options.DryRun && needsRecord {
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
	if result.Promotion.Status != "would_promote" {
		return result, nil
	}
	if options.DryRun {
		result.Promotion = checkPromotionLabel(ctx, backend, repo, result.Promotion)
		return result, nil
	}
	if options.SelfHosted {
		result.Promotion = applyHostedPromotion(ctx, backend, repo, number, policy.PriorityLabel, policyRef)
		return result, nil
	}
	return applyGitHubPromotion(ctx, backend, result, policyRef), nil
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
	return collectObservations(issue, comments, policy), nil
}

func collectObservations(issue github.Issue, comments []github.Comment, policy Policy) snapshot {
	state := snapshot{issue: issue, events: make(map[string]occurrence)}
	state.addBody(issue.Body, policy.trusts(issue.User), 0)
	for _, comment := range comments {
		state.addBody(comment.Body, policy.trusts(comment.User), comment.ID)
	}
	return state
}

// EvaluatePromotion makes the same decision from backend-authoritative Issue and
// comment state, including when the self-hosted server owns the label mutation.
func EvaluatePromotion(issue github.Issue, comments []github.Comment, policy Policy) Promotion {
	return promotionPlan(issue, policy, collectObservations(issue, comments, policy).confirmed)
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

func applyGitHubPromotion(ctx context.Context, backend Backend, result Result, policyRef string) Result {
	policy, _, currentRef, err := loadPolicy(ctx, backend, result.Repository)
	if err != nil || currentRef != policyRef {
		result.CountStale = true
		result.Promotion.Status = "pending"
		result.Promotion.Reason = "published feedback policy changed"
		if err != nil {
			result.Promotion.Reason = fmt.Sprintf("recheck feedback policy: %v", err)
		}
		return result
	}

	current, err := observe(ctx, backend, result.Repository, result.Issue, policy)
	if err != nil {
		result.CountStale = true
		result.Promotion.Status = "pending"
		result.Promotion.Reason = fmt.Sprintf("recheck feedback evidence and Issue state: %v", err)
		return result
	}
	result.setOccurrence(current.events[result.EventKey], current.confirmed)
	result.Promotion = promotionPlan(current.issue, policy, current.confirmed)
	if result.Promotion.Status != "would_promote" {
		return result
	}

	result.Promotion = checkPromotionLabel(ctx, backend, result.Repository, result.Promotion)
	if result.Promotion.Status != "would_promote" {
		return result
	}
	labels, err := backend.AddIssueLabels(ctx, result.Repository, result.Issue, []string{policy.PriorityLabel})
	if err != nil {
		result.Promotion = reconcilePromotion(ctx, backend, result.Repository, result.Issue, policy.PriorityLabel, err)
		return result
	}
	if !hasLabel(labels, policy.PriorityLabel) {
		result.Promotion.Status = "pending"
		result.Promotion.Reason = "the backend did not assign the label; check label existence and triage permission"
		return result
	}
	result.Promotion.Status = "applied"
	return result
}

func checkPromotionLabel(ctx context.Context, backend github.IssueLabelBackend, repo string, plan Promotion) Promotion {
	if _, err := backend.GetRepositoryLabel(ctx, repo, plan.Label); err != nil {
		plan.Status = "pending"
		plan.Reason = fmt.Sprintf("configured priority label unavailable: %v", err)
	}
	return plan
}

func applyHostedPromotion(ctx context.Context, backend Backend, repo string, number int, label, policyRef string) Promotion {
	promoter, ok := backend.(github.FeedbackPromotionBackend)
	if !ok {
		return Promotion{Label: label, Status: "pending", Reason: "the selected self-hosted backend does not support feedback promotion"}
	}
	result, err := promoter.PromoteFeedback(ctx, repo, number, policyRef)
	if err != nil {
		return reconcilePromotion(ctx, backend, repo, number, label, err)
	}
	if result.Label == "" {
		result.Label = label
	}
	return result
}

func reconcilePromotion(ctx context.Context, backend github.IssueBackend, repo string, number int, label string, err error) Promotion {
	// The provider may have accepted an addition whose response was lost.
	if current, readErr := backend.GetIssue(ctx, repo, number); readErr == nil && hasLabel(current.Labels, label) {
		return Promotion{Label: label, Status: "applied"}
	}
	return Promotion{Label: label, Status: "pending", Reason: err.Error()}
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
