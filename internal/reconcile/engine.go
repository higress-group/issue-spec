package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

type Backend interface {
	github.IssueBackend
}

type Engine struct{ Backend Backend }

type observed struct {
	Comment github.Comment
	Body    string
	Version int64
}

func (e Engine) Run(ctx context.Context, plan Plan, checkpointPath string) (Result, error) {
	ordered, digest, err := Validate(plan)
	result := Result{PlanDigest: digest, Checkpoint: checkpointPath, Atomic: !plan.AllowNonAtomic}
	if err != nil {
		return result, err
	}
	cp, err := LoadCheckpoint(checkpointPath, digest)
	if err != nil {
		return result, err
	}
	// Observe and simulate the complete plan before the first write. This catches
	// duplicate markers and illegal transitions even when they occur late in the DAG.
	state, err := e.preflight(ctx, plan, ordered)
	if err != nil {
		return result, err
	}
	_ = state
	states := map[string]string{}
	for _, op := range ordered {
		blocked := false
		for _, dep := range op.DependsOn {
			if states[dep] == "pending" || states[dep] == "conflicted" {
				blocked = true
			}
		}
		var r OperationResult
		if blocked {
			r = OperationResult{ID: op.ID, Kind: op.Kind, Status: "pending", Atomic: !plan.AllowNonAtomic, Message: "dependency is not complete"}
		} else {
			r = e.apply(ctx, plan, op)
		}
		states[op.ID] = r.Status
		result.Operations = append(result.Operations, r)
		switch r.Status {
		case "created":
			result.Created++
		case "updated":
			result.Updated++
		case "unchanged":
			result.Unchanged++
		case "conflicted":
			result.Conflicted++
		case "pending":
			result.Pending++
		}
		if r.Status == "created" || r.Status == "updated" || r.Status == "unchanged" {
			cp.Completed[op.ID] = r.Status
			if err := SaveCheckpoint(checkpointPath, cp); err != nil {
				return result, fmt.Errorf("save checkpoint after %s: %w", op.ID, err)
			}
		}
	}
	result.OK = result.Conflicted == 0 && result.Pending == 0
	return result, nil
}

func (e Engine) preflight(ctx context.Context, plan Plan, ordered []Operation) (map[string]string, error) {
	state := map[string]string{}
	for _, op := range ordered {
		for _, target := range operationTargets(op) {
			key := targetKey(target)
			if _, ok := state[key]; ok {
				continue
			}
			item, found, err := e.observe(ctx, plan.Repo, target, false)
			if err != nil {
				return nil, fmt.Errorf("prewrite observe %s: %w", key, err)
			}
			if found {
				state[key] = item.Body
			} else {
				state[key] = ""
			}
		}
		switch op.Kind {
		case "upsert":
			tc := model.ParseTypedComment(op.Desired.Body)
			if !model.HasTypedMarker(op.Desired.Body) || tc.Type != strings.ToUpper(op.Target.Type) || tc.ID != op.Target.ID {
				return nil, fmt.Errorf("operation %s desired body marker/type/id does not match target", op.ID)
			}
			state[targetKey(op.Target)] = mergeRelated(state[targetKey(op.Target)], op.Desired.Body)
		case "transition":
			body := state[targetKey(op.Target)]
			if body == "" {
				return nil, fmt.Errorf("operation %s transition target is absent", op.ID)
			}
			tr, err := applyTransition(body, op)
			if err != nil {
				return nil, fmt.Errorf("operation %s: %w", op.ID, err)
			}
			state[targetKey(op.Target)] = tr.Body
		case "link":
			left, right := state[targetKey(op.Target)], state[targetKey(*op.Desired.Peer)]
			if left == "" || right == "" {
				return nil, fmt.Errorf("operation %s link target is absent", op.ID)
			}
			leftURL, rightURL := artifactURL(plan.Repo, op.Target, 1), artifactURL(plan.Repo, *op.Desired.Peer, 1)
			left, _, err := model.AddRelatedCommentLink(left, rightURL)
			if err != nil {
				return nil, err
			}
			right, _, err = model.AddRelatedCommentLink(right, leftURL)
			if err != nil {
				return nil, err
			}
			state[targetKey(op.Target)], state[targetKey(*op.Desired.Peer)] = left, right
		}
	}
	return state, nil
}

func (e Engine) apply(ctx context.Context, plan Plan, op Operation) OperationResult {
	switch op.Kind {
	case "upsert":
		return e.applyUpsert(ctx, plan, op)
	case "transition":
		return e.applyTransition(ctx, plan, op)
	case "link":
		return e.applyLink(ctx, plan, op)
	default:
		return conflictResult(op, "unsupported operation")
	}
}

func (e Engine) applyUpsert(ctx context.Context, plan Plan, op Operation) OperationResult {
	item, found, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	desired := op.Desired.Body
	if found {
		desired = mergeRelated(item.Body, desired)
	}
	if found && item.Body == desired {
		return unchangedResult(op, item, !plan.AllowNonAtomic)
	}
	if !found {
		created, err := e.Backend.CreateComment(ctx, plan.Repo, op.Target.Issue, desired)
		if err != nil {
			return failureResult(op, err, false)
		}
		return OperationResult{ID: op.ID, Kind: op.Kind, Status: "created", Atomic: false, CommentID: created.ID, URL: created.HTMLURL}
	}
	return e.mutate(ctx, plan, op, item, desired)
}

func (e Engine) applyTransition(ctx context.Context, plan Plan, op Operation) OperationResult {
	item, found, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	if !found {
		return conflictResult(op, "transition target is absent")
	}
	tr, err := applyTransition(item.Body, op)
	if err != nil {
		return conflictResult(op, err.Error())
	}
	if !tr.Changed {
		return unchangedResult(op, item, !plan.AllowNonAtomic)
	}
	return e.mutate(ctx, plan, op, item, tr.Body)
}

func (e Engine) applyLink(ctx context.Context, plan Plan, op Operation) OperationResult {
	left, leftFound, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	right, rightFound, err := e.observe(ctx, plan.Repo, *op.Desired.Peer, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	if !leftFound || !rightFound {
		return conflictResult(op, "link target is absent")
	}
	leftBody, leftChanged, err := model.AddRelatedCommentLink(left.Body, right.Comment.HTMLURL)
	if err != nil {
		return conflictResult(op, err.Error())
	}
	rightBody, rightChanged, err := model.AddRelatedCommentLink(right.Body, left.Comment.HTMLURL)
	if err != nil {
		return conflictResult(op, err.Error())
	}
	if leftChanged {
		r := e.mutate(ctx, plan, op, left, leftBody)
		if r.Status != "updated" && r.Status != "unchanged" {
			return r
		}
	}
	if rightChanged {
		// Re-observation here is intentional: a failed second write leaves a
		// recoverable half-link and resume only applies the missing direction.
		right, _, err = e.observe(ctx, plan.Repo, *op.Desired.Peer, true)
		if err != nil {
			return failureResult(op, err, !plan.AllowNonAtomic)
		}
		r := e.mutate(ctx, plan, op, right, rightBody)
		if r.Status != "updated" && r.Status != "unchanged" {
			return r
		}
	}
	if !leftChanged && !rightChanged {
		return unchangedResult(op, left, !plan.AllowNonAtomic)
	}
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: !plan.AllowNonAtomic, CommentID: left.Comment.ID, URL: left.Comment.HTMLURL}
}

func (e Engine) mutate(ctx context.Context, plan Plan, op Operation, item observed, body string) OperationResult {
	if err := checkPrecondition(op.Precondition, item); err != nil {
		return conflictResult(op, err.Error())
	}
	conditional, ok := e.Backend.(github.ConditionalCommentBackend)
	if ok && item.Version > 0 {
		updated, err := conditional.UpdateCommentConditional(ctx, plan.Repo, item.Comment.ID, item.Version, body)
		if err == nil {
			return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: true, CommentID: updated.Comment.ID, URL: updated.Comment.HTMLURL}
		}
		if !errors.Is(err, github.ErrConditionalCommentMutationUnsupported) {
			return failureResult(op, err, true)
		}
	}
	if !plan.AllowNonAtomic {
		return conflictResult(op, "conditional mutation unsupported; plan allow_nonatomic is false")
	}
	if op.Precondition.RepresentationVersion > 0 {
		return conflictResult(op, "representation version requires conditional mutation")
	}
	updated, err := e.Backend.UpdateComment(ctx, plan.Repo, item.Comment.ID, body)
	if err != nil {
		return failureResult(op, err, false)
	}
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: false, CommentID: updated.ID, URL: updated.HTMLURL}
}

func (e Engine) observe(ctx context.Context, repo string, target Target, exact bool) (observed, bool, error) {
	comments, err := e.Backend.ListIssueComments(ctx, repo, target.Issue)
	if err != nil {
		return observed{}, false, err
	}
	var matches []github.Comment
	for _, comment := range comments {
		tc := model.ParseTypedComment(comment.Body)
		if tc.Type == strings.ToUpper(target.Type) && tc.ID == target.ID {
			matches = append(matches, comment)
		}
	}
	if len(matches) > 1 {
		return observed{}, false, fmt.Errorf("duplicate logical marker %s", targetKey(target))
	}
	if len(matches) == 0 {
		return observed{}, false, nil
	}
	item := observed{Comment: matches[0], Body: matches[0].Body}
	if exact {
		if conditional, ok := e.Backend.(github.ConditionalCommentBackend); ok {
			rep, err := conditional.GetCommentRepresentation(ctx, repo, matches[0].ID)
			if err != nil && !errors.Is(err, github.ErrConditionalCommentMutationUnsupported) {
				return observed{}, false, err
			}
			if err == nil {
				item.Comment, item.Body, item.Version = rep.Comment, rep.Comment.Body, rep.RepresentationVersion
			}
		}
	}
	return item, true, nil
}

func applyTransition(body string, op Operation) (model.TransitionResult, error) {
	var handoff *model.HandoffMutation
	if op.Desired.Handoff != "" {
		handoff = &model.HandoffMutation{Value: op.Desired.Handoff, Append: op.Desired.AppendHandoff}
	}
	return model.ApplyTypedTransition(body, model.TransitionRequest{ExpectedType: op.Target.Type, ExpectedID: op.Target.ID,
		ToStatus: op.Desired.Status, Handoff: handoff, PRLinks: op.Desired.PRLinks, RelatedLinks: op.Desired.RelatedLinks})
}

func operationTargets(op Operation) []Target {
	if op.Desired.Peer != nil {
		return []Target{op.Target, *op.Desired.Peer}
	}
	return []Target{op.Target}
}
func artifactURL(repo string, target Target, commentID int64) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", repo, target.Issue, commentID)
}
func mergeRelated(before, desired string) string {
	if before == "" {
		return desired
	}
	for _, url := range model.RelatedCommentURLs(model.ParseTypedComment(before)) {
		if next, _, err := model.AddRelatedCommentLink(desired, url); err == nil {
			desired = next
		}
	}
	return desired
}
func checkPrecondition(p Precondition, item observed) error {
	if p.RepresentationVersion > 0 && p.RepresentationVersion != item.Version {
		return fmt.Errorf("representation conflict: expected=%d current=%d", p.RepresentationVersion, item.Version)
	}
	if p.BodyDigest != "" && normalizeDigest(p.BodyDigest) != bodyDigest(item.Body) {
		return fmt.Errorf("body digest conflict: expected=%s current=%s", normalizeDigest(p.BodyDigest), bodyDigest(item.Body))
	}
	return nil
}
func bodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
func unchangedResult(op Operation, item observed, atomic bool) OperationResult {
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "unchanged", Atomic: atomic, CommentID: item.Comment.ID, URL: item.Comment.HTMLURL}
}
func conflictResult(op Operation, message string) OperationResult {
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "conflicted", Message: message}
}
func failureResult(op Operation, err error, atomic bool) OperationResult {
	status := "conflicted"
	if retryable(err) {
		status = "pending"
	}
	var conflict *github.CommentMutationConflictError
	if errors.As(err, &conflict) {
		status = "conflicted"
	}
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: status, Atomic: atomic, Message: err.Error()}
}
func retryable(err error) bool {
	var api *github.APIError
	if errors.As(err, &api) && (api.StatusCode == http.StatusTooManyRequests || api.StatusCode >= 500) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "rate limit") || strings.Contains(text, "timeout") || strings.Contains(text, "temporary")
}
