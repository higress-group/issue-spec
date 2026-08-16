package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/relationships"
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
	result := Result{PlanDigest: digest, Checkpoint: checkpointPath, Atomic: true}
	if err != nil {
		return result, err
	}
	cp, err := LoadCheckpoint(checkpointPath, digest)
	if err != nil {
		return result, err
	}
	if plan.Version == LegacyPlanVersion {
		for _, op := range ordered {
			if op.Kind != "link" || cp.Completed[op.ID] != "" {
				continue
			}
			result.Atomic = false
			result.Conflicted = 1
			result.Remediation = "legacy_link_plan_requires_replan"
			result.Operations = []OperationResult{{ID: op.ID, Kind: op.Kind, Status: "conflicted", Atomic: false,
				Guarantee: github.CommentMutationNonAtomicSingleWriter, Message: "legacy_link_plan_requires_replan"}}
			return result, nil
		}
	}
	preflightOperations := make([]Operation, 0, len(ordered))
	for _, op := range ordered {
		if plan.Version == LegacyPlanVersion && op.Kind == "link" && cp.Completed[op.ID] != "" {
			continue
		}
		preflightOperations = append(preflightOperations, op)
	}
	// Observe and simulate the complete plan before the first write. This catches
	// duplicate markers and illegal transitions even when they occur late in the DAG.
	state, err := e.preflight(ctx, plan, preflightOperations)
	if err != nil {
		return result, err
	}
	_ = state
	states := map[string]string{}
	for _, op := range ordered {
		if plan.Version == LegacyPlanVersion && op.Kind == "link" && cp.Completed[op.ID] != "" {
			status := cp.Completed[op.ID]
			states[op.ID] = status
			result.Operations = append(result.Operations, OperationResult{ID: op.ID, Kind: op.Kind, Status: status,
				Atomic: false, Guarantee: github.CommentMutationNonAtomicSingleWriter, Message: "completed legacy v1 checkpoint history"})
			switch status {
			case "created":
				result.Created++
			case "updated":
				result.Updated++
			default:
				result.Unchanged++
			}
			result.Atomic = false
			continue
		}
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
		if !r.Atomic {
			result.Atomic = false
		}
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
	observations := map[string]observed{}
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
				observations[key] = item
			} else {
				state[key] = ""
			}
		}
		if err := checkAcceptedReceiptPrecondition(op.Precondition, state[targetKey(op.Target)]); err != nil {
			return nil, fmt.Errorf("operation %s: %w", op.ID, err)
		}
		switch op.Kind {
		case "upsert":
			tc := model.ParseTypedComment(op.Desired.Body)
			if !model.HasTypedMarker(op.Desired.Body) || tc.Type != strings.ToUpper(op.Target.Type) || tc.ID != op.Target.ID {
				return nil, fmt.Errorf("operation %s desired body marker/type/id does not match target", op.ID)
			}
			merged := op.Desired.Body
			if current := state[targetKey(op.Target)]; current != "" {
				var mergeErr error
				merged, _, mergeErr = model.MergeTypedHeaderRelationships(current, op.Desired.Body)
				if mergeErr != nil {
					return nil, fmt.Errorf("operation %s: %w", op.ID, mergeErr)
				}
			}
			state[targetKey(op.Target)] = merged
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
			if op.Desired.CarrierAuthorizedBacklink {
				peer, peerFound := observations[targetKey(*op.Desired.Peer)]
				carrier, ok := observations[targetKey(op.Target)]
				if !ok {
					return nil, fmt.Errorf("operation %s: accepted receipt carrier has no provider identity", op.ID)
				}
				if err := checkAcceptedReceiptPrecondition(op.Precondition, carrier.Body); err != nil {
					return nil, fmt.Errorf("operation %s: %w", op.ID, err)
				}
				if !peerFound {
					return nil, fmt.Errorf("operation %s: relationship target %s has no provider identity", op.ID, projectionTargetKey(*op.Desired.Peer))
				}
				if authority := op.Precondition.RelationshipAuthority; authority != nil {
					assignmentProcess, found := observations[targetKey(*authority.AssignmentProcess)]
					if !found {
						return nil, fmt.Errorf("operation %s: assignment PROCESS authority is absent", op.ID)
					}
					if err := checkRelationshipAuthority(op, carrier, peer, assignmentProcess); err != nil {
						return nil, fmt.Errorf("operation %s: %w", op.ID, err)
					}
				} else if !hasRelatedCommentLink(carrier.Body, peer.Comment) {
					return nil, fmt.Errorf("operation %s: relationship target %s is not explicitly authorized by the accepted receipt carrier", op.ID, projectionTargetKey(*op.Desired.Peer))
				}
				right, _, err := model.AddRelatedCommentLink(right, carrier.Comment.HTMLURL)
				if err != nil {
					return nil, err
				}
				state[targetKey(*op.Desired.Peer)] = right
				continue
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
		case "relationship-update":
			owner := state[targetKey(op.Target)]
			if owner == "" {
				return nil, fmt.Errorf("operation %s relationship owner is absent", op.ID)
			}
			observedOwner, found := observations[targetKey(op.Target)]
			if !found {
				return nil, fmt.Errorf("operation %s relationship owner has no provider identity", op.ID)
			}
			if authority := op.Precondition.RelationshipAuthority; authority != nil {
				assignmentProcess, found := observations[targetKey(*authority.AssignmentProcess)]
				if !found {
					return nil, fmt.Errorf("operation %s: assignment PROCESS authority is absent", op.ID)
				}
				if err := checkRelationshipOwnerAuthority(op, observedOwner, assignmentProcess); err != nil {
					return nil, fmt.Errorf("operation %s: %w", op.ID, err)
				}
			}
			mutation, err := relationshipMutation(op, observedOwner, owner)
			if err != nil {
				return nil, fmt.Errorf("operation %s: %w", op.ID, err)
			}
			desired, _, err := relationships.ApplyMutation(owner, mutation)
			if err != nil {
				return nil, fmt.Errorf("operation %s: %w", op.ID, err)
			}
			state[targetKey(op.Target)] = desired
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
	case "relationship-update":
		return e.applyRelationshipUpdate(ctx, plan, op)
	default:
		return conflictResult(op, "unsupported operation")
	}
}

func (e Engine) applyRelationshipUpdate(ctx context.Context, plan Plan, op Operation) OperationResult {
	owner, found, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	if !found {
		return conflictResult(op, "relationship owner is absent")
	}
	if err := checkAcceptedReceiptPrecondition(op.Precondition, owner.Body); err != nil {
		return observedConflictResult(op, err.Error(), owner)
	}
	if authority := op.Precondition.RelationshipAuthority; authority != nil {
		assignmentProcess, assignmentFound, observeErr := e.observe(ctx, plan.Repo, *authority.AssignmentProcess, true)
		if observeErr != nil {
			return failureResult(op, observeErr, !plan.AllowNonAtomic)
		}
		if !assignmentFound {
			return conflictResult(op, "assignment PROCESS authority is absent")
		}
		if err := checkRelationshipOwnerAuthority(op, owner, assignmentProcess); err != nil {
			return observedConflictResult(op, err.Error(), owner)
		}
	}
	mutation, err := relationshipMutation(op, owner, owner.Body)
	if err != nil {
		return observedConflictResult(op, err.Error(), owner)
	}
	desired, changed, err := relationships.ApplyMutation(owner.Body, mutation)
	if err != nil {
		return observedConflictResult(op, err.Error(), owner)
	}
	if !changed {
		return unchangedResult(op, owner, owner.Version > 0)
	}
	return e.mutate(ctx, plan, op, op.Target, owner, desired)
}

func relationshipMutation(op Operation, owner observed, body string) (relationships.Mutation, error) {
	update := op.Desired.RelationshipUpdate
	if update == nil {
		return relationships.Mutation{}, errors.New("relationship update is missing")
	}
	ownerRef := model.ArtifactRef{Issue: op.Target.Issue, Type: strings.ToUpper(op.Target.Type), ID: op.Target.ID,
		CommentID: owner.Comment.ID, URL: model.NormalizeURL(owner.Comment.HTMLURL)}
	if err := ownerRef.Validate(); err != nil {
		return relationships.Mutation{}, fmt.Errorf("owner provider identity: %w", err)
	}
	convert := func(values []RelationshipTarget) ([]model.ArtifactRef, error) {
		result := make([]model.ArtifactRef, 0, len(values))
		for _, value := range values {
			ref := model.ArtifactRef{Issue: value.Target.Issue, Type: strings.ToUpper(value.Target.Type),
				ID: value.Target.ID, URL: model.NormalizeURL(value.URL)}
			if err := ref.Validate(); err != nil {
				return nil, fmt.Errorf("relationship target %s provider identity: %w", value.Target.ID, err)
			}
			result = append(result, ref)
		}
		return result, nil
	}
	add, err := convert(update.Add)
	if err != nil {
		return relationships.Mutation{}, err
	}
	remove, err := convert(update.Remove)
	if err != nil {
		return relationships.Mutation{}, err
	}
	_ = body
	return relationships.Mutation{Version: relationships.MutationVersion, Owner: ownerRef, Add: add, Remove: remove}, nil
}

func (e Engine) applyUpsert(ctx context.Context, plan Plan, op Operation) OperationResult {
	item, found, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	desired := op.Desired.Body
	if found {
		desired, _, err = model.MergeTypedHeaderRelationships(item.Body, desired)
		if err != nil {
			return observedConflictResult(op, err.Error(), item)
		}
	}
	if found && item.Body == desired {
		return unchangedResult(op, item, !plan.AllowNonAtomic)
	}
	if !found {
		if !plan.AllowNonAtomic {
			return conflictResult(op, "comment creation is non-atomic; plan allow_nonatomic is false")
		}
		fresh, freshFound, observeErr := e.observe(ctx, plan.Repo, op.Target, true)
		if observeErr != nil {
			return failureResult(op, fmt.Errorf("pre-create exact observation: %w", observeErr), false)
		}
		if freshFound {
			if fresh.Body == desired {
				return unchangedResult(op, fresh, false)
			}
			return conflictResult(op, "comment appeared after initial observation; refusing non-atomic creation")
		}
		_, err := e.Backend.CreateComment(ctx, plan.Repo, op.Target.Issue, desired)
		if err != nil {
			if _, _, observeErr := e.observe(ctx, plan.Repo, op.Target, true); observeErr != nil {
				return failureResult(op, fmt.Errorf("create outcome uncertain: %v; re-observe: %w", err, observeErr), false)
			}
			return failureResult(op, err, false)
		}
		observedCreated, createdFound, observeErr := e.observe(ctx, plan.Repo, op.Target, true)
		if observeErr != nil {
			return failureResult(op, fmt.Errorf("create succeeded but re-observe failed: %w", observeErr), false)
		}
		if !createdFound || observedCreated.Body != desired {
			return conflictResult(op, "create succeeded but exact re-observation did not match the planned comment")
		}
		return OperationResult{ID: op.ID, Kind: op.Kind, Status: "created", Atomic: false,
			Guarantee: github.CommentMutationNonAtomicSingleWriter,
			CommentID: observedCreated.Comment.ID, URL: observedCreated.Comment.HTMLURL,
			AfterDigest: model.RepresentationDigest(observedCreated.Body)}
	}
	return e.mutate(ctx, plan, op, op.Target, item, desired)
}

func (e Engine) applyTransition(ctx context.Context, plan Plan, op Operation) OperationResult {
	item, found, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	if !found {
		return conflictResult(op, "transition target is absent")
	}
	if err := checkAcceptedReceiptPrecondition(op.Precondition, item.Body); err != nil {
		return observedConflictResult(op, err.Error(), item)
	}
	tr, err := applyTransition(item.Body, op)
	if err != nil {
		return observedConflictResult(op, err.Error(), item)
	}
	if !tr.Changed {
		return unchangedResult(op, item, !plan.AllowNonAtomic)
	}
	return e.mutate(ctx, plan, op, op.Target, item, tr.Body)
}

func (e Engine) applyLink(ctx context.Context, plan Plan, op Operation) OperationResult {
	if op.Desired.CarrierAuthorizedBacklink {
		return e.applyCarrierAuthorizedBacklink(ctx, plan, op)
	}
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
		return observedConflictResult(op, err.Error(), left)
	}
	rightBody, rightChanged, err := model.AddRelatedCommentLink(right.Body, left.Comment.HTMLURL)
	if err != nil {
		return observedConflictResult(op, err.Error(), right)
	}
	if err := checkLinkEndpointPrecondition(op, op.Target, left, leftBody, leftChanged); err != nil {
		return observedConflictResult(op, err.Error(), left)
	}
	if err := checkLinkEndpointPrecondition(op, *op.Desired.Peer, right, rightBody, rightChanged); err != nil {
		return observedConflictResult(op, err.Error(), right)
	}
	mutations := 0
	atomic := true
	var endpointResults []EndpointResult
	if leftChanged {
		r := e.mutate(ctx, plan, op, op.Target, left, leftBody)
		if r.Status != "updated" && r.Status != "unchanged" {
			return r
		}
		if r.Status == "updated" {
			mutations++
			atomic = atomic && r.Atomic
			endpointResults = append(endpointResults, endpointResult(op.Target, r))
		}
	}
	if rightChanged {
		// Re-observation here is intentional: a failed second write leaves a
		// recoverable half-link and resume only applies the missing direction.
		right, _, err = e.observe(ctx, plan.Repo, *op.Desired.Peer, true)
		if err != nil {
			return failureResult(op, err, !plan.AllowNonAtomic)
		}
		rightBody, stillChanged, linkErr := model.AddRelatedCommentLink(right.Body, left.Comment.HTMLURL)
		if linkErr != nil {
			r := observedConflictResult(op, linkErr.Error(), right)
			r.Endpoints = endpointResults
			return r
		}
		if endpointErr := checkLinkEndpointPrecondition(op, *op.Desired.Peer, right, rightBody, stillChanged); endpointErr != nil {
			r := observedConflictResult(op, endpointErr.Error(), right)
			r.Endpoints = endpointResults
			return r
		}
		if !stillChanged {
			rightChanged = false
		} else {
			r := e.mutate(ctx, plan, op, *op.Desired.Peer, right, rightBody)
			if r.Status != "updated" && r.Status != "unchanged" {
				if mutations > 0 {
					r.Atomic = false
				}
				r.Endpoints = append(endpointResults, r.Endpoints...)
				return r
			}
			if r.Status == "updated" {
				mutations++
				atomic = atomic && r.Atomic
				endpointResults = append(endpointResults, endpointResult(*op.Desired.Peer, r))
			}
		}
	}
	if !leftChanged && !rightChanged {
		return unchangedResult(op, left, !plan.AllowNonAtomic)
	}
	if mutations > 1 {
		atomic = false
	}
	confirmed, found, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		r := observedFailureResult(op, fmt.Errorf("link succeeded but exact re-observation failed: %w", err), atomic, left)
		r.Endpoints = endpointResults
		return r
	}
	if !found || confirmed.Comment.ID != left.Comment.ID {
		r := observedConflictResult(op, "link succeeded but the exact result target could not be re-observed", left)
		r.Endpoints = endpointResults
		return r
	}
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: atomic,
		Guarantee: mutationGuarantee(atomic),
		CommentID: confirmed.Comment.ID, URL: confirmed.Comment.HTMLURL,
		BeforeDigest: model.RepresentationDigest(left.Body), AfterDigest: model.RepresentationDigest(confirmed.Body),
		Endpoints: endpointResults}
}

func (e Engine) applyCarrierAuthorizedBacklink(ctx context.Context, plan Plan, op Operation) OperationResult {
	carrier, carrierFound, err := e.observe(ctx, plan.Repo, op.Target, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	peer, peerFound, err := e.observe(ctx, plan.Repo, *op.Desired.Peer, true)
	if err != nil {
		return failureResult(op, err, !plan.AllowNonAtomic)
	}
	if !carrierFound || !peerFound {
		return conflictResult(op, "carrier-authorized backlink target is absent")
	}
	if err := checkAcceptedReceiptPrecondition(op.Precondition, carrier.Body); err != nil {
		return observedConflictResult(op, err.Error(), carrier)
	}
	if authority := op.Precondition.RelationshipAuthority; authority != nil {
		assignmentProcess, found, observeErr := e.observe(ctx, plan.Repo, *authority.AssignmentProcess, true)
		if observeErr != nil {
			return failureResult(op, observeErr, !plan.AllowNonAtomic)
		}
		if !found {
			return conflictResult(op, "assignment PROCESS authority is absent")
		}
		if err := checkRelationshipAuthority(op, carrier, peer, assignmentProcess); err != nil {
			return conflictResult(op, err.Error())
		}
	} else if !hasRelatedCommentLink(carrier.Body, peer.Comment) {
		return conflictResult(op, fmt.Sprintf("relationship target %s is not explicitly authorized by the accepted receipt carrier", projectionTargetKey(*op.Desired.Peer)))
	}
	peerBody, changed, err := model.AddRelatedCommentLink(peer.Body, carrier.Comment.HTMLURL)
	if err != nil {
		return observedConflictResult(op, err.Error(), peer)
	}
	if err := checkLinkEndpointPrecondition(op, *op.Desired.Peer, peer, peerBody, changed); err != nil {
		return observedConflictResult(op, err.Error(), peer)
	}
	if !changed {
		return unchangedResult(op, peer, !plan.AllowNonAtomic)
	}
	result := e.mutate(ctx, plan, op, *op.Desired.Peer, peer, peerBody)
	if result.Status == "updated" {
		result.Endpoints = []EndpointResult{endpointResult(*op.Desired.Peer, result)}
	}
	return result
}

func (e Engine) mutate(ctx context.Context, plan Plan, op Operation, target Target, item observed, body string) OperationResult {
	if err := checkMutationPrecondition(op, target, item); err != nil {
		return observedConflictResult(op, err.Error(), item)
	}
	conditional, ok := e.Backend.(github.ConditionalCommentBackend)
	if ok && item.Version > 0 {
		// A translation-bot edit between observation and mutation bumps the
		// representation version; that conflict resolves through the existing
		// retry/re-observe flow and is deliberately not silenced here.
		_, err := conditional.UpdateCommentConditional(ctx, plan.Repo, item.Comment.ID, item.Version, body)
		if err == nil {
			confirmed, found, observeErr := e.observe(ctx, plan.Repo, target, true)
			if observeErr != nil {
				return observedFailureResult(op, fmt.Errorf("conditional update succeeded but exact re-observation failed: %w", observeErr), true, item)
			}
			if !found || confirmed.Comment.ID != item.Comment.ID {
				return observedConflictResult(op, "conditional update succeeded but the exact target could not be re-observed", item)
			}
			if !model.CanonicalBodyEqual(confirmed.Body, body) {
				return OperationResult{ID: op.ID, Kind: op.Kind, Status: "conflicted", Atomic: true,
					Guarantee: github.CommentMutationStrictConditional,
					CommentID: confirmed.Comment.ID, URL: confirmed.Comment.HTMLURL,
					BeforeDigest: model.RepresentationDigest(item.Body), AfterDigest: model.RepresentationDigest(confirmed.Body),
					Message: fmt.Sprintf("conditional update returned but canonical representation digest did not match: expected=%s current=%s",
						model.RepresentationDigest(body), model.RepresentationDigest(confirmed.Body))}
			}
			return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: true,
				Guarantee: github.CommentMutationStrictConditional,
				CommentID: confirmed.Comment.ID, URL: confirmed.Comment.HTMLURL,
				BeforeDigest: model.RepresentationDigest(item.Body), AfterDigest: model.RepresentationDigest(confirmed.Body)}
		}
		if !errors.Is(err, github.ErrConditionalCommentMutationUnsupported) {
			current, found, observeErr := e.observe(ctx, plan.Repo, target, true)
			if observeErr != nil {
				return observedFailureResult(op, fmt.Errorf("conditional update outcome uncertain: %v; exact re-observation: %w", err, observeErr), true, item)
			}
			if found && current.Comment.ID == item.Comment.ID && model.CanonicalBodyEqual(current.Body, body) {
				return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: true,
					Guarantee: github.CommentMutationStrictConditional, CommentID: current.Comment.ID, URL: current.Comment.HTMLURL,
					BeforeDigest: model.RepresentationDigest(item.Body), AfterDigest: model.RepresentationDigest(current.Body),
					Message: "conditional update response was lost; canonical postcondition observed"}
			}
			if found && current.Comment.ID == item.Comment.ID {
				return observedPairFailureResult(op, err, true, item, current)
			}
			return observedFailureResult(op, err, true, item)
		}
	}
	if !plan.AllowNonAtomic {
		return observedConflictResult(op, "conditional mutation unsupported; plan allow_nonatomic is false", item)
	}
	if op.Kind == "relationship-update" && normalizeDigest(op.Precondition.BodyDigest) == "" {
		return observedConflictResult(op, "non-atomic relationship update requires an exact expected body_digest precondition", item)
	}
	if op.Precondition.RepresentationVersion > 0 {
		return observedConflictResult(op, "representation version requires conditional mutation", item)
	}
	// A non-conditional provider cannot close the final compare-and-swap race,
	// but it must still use the freshest exact representation as a digest guard
	// and checkpoint only an exactly re-observed write.
	fresh, found, err := e.observe(ctx, plan.Repo, target, true)
	if err != nil {
		return observedFailureResult(op, fmt.Errorf("pre-update exact observation: %w", err), false, item)
	}
	if !found || fresh.Comment.ID != item.Comment.ID {
		return observedConflictResult(op, "mutation target changed after initial observation", item)
	}
	if model.RepresentationDigest(fresh.Body) != model.RepresentationDigest(item.Body) {
		return OperationResult{ID: op.ID, Kind: op.Kind, Status: "conflicted", Atomic: false,
			Guarantee: github.CommentMutationNonAtomicSingleWriter,
			CommentID: fresh.Comment.ID, URL: fresh.Comment.HTMLURL,
			BeforeDigest: model.RepresentationDigest(item.Body), AfterDigest: model.RepresentationDigest(fresh.Body),
			Message: fmt.Sprintf("representation digest changed after initial observation: expected=%s current=%s",
				model.RepresentationDigest(item.Body), model.RepresentationDigest(fresh.Body))}
	}
	if err := checkMutationPrecondition(op, target, fresh); err != nil {
		return observedConflictResult(op, err.Error(), fresh)
	}
	_, err = e.Backend.UpdateComment(ctx, plan.Repo, item.Comment.ID, body)
	confirmed, confirmedFound, observeErr := e.observe(ctx, plan.Repo, target, true)
	if observeErr != nil {
		if err != nil {
			return observedFailureResult(op, fmt.Errorf("update outcome uncertain: %v; exact re-observation: %w", err, observeErr), false, item)
		}
		return observedFailureResult(op, fmt.Errorf("update succeeded but exact re-observation failed: %w", observeErr), false, item)
	}
	if confirmedFound && confirmed.Comment.ID == item.Comment.ID && model.CanonicalBodyEqual(confirmed.Body, body) {
		return OperationResult{ID: op.ID, Kind: op.Kind, Status: "updated", Atomic: false,
			Guarantee: github.CommentMutationNonAtomicSingleWriter,
			CommentID: confirmed.Comment.ID, URL: confirmed.Comment.HTMLURL,
			BeforeDigest: model.RepresentationDigest(item.Body), AfterDigest: model.RepresentationDigest(confirmed.Body)}
	}
	if err != nil {
		if confirmedFound && confirmed.Comment.ID == item.Comment.ID {
			return observedPairFailureResult(op, err, false, item, confirmed)
		}
		return observedFailureResult(op, err, false, item)
	}
	if !confirmedFound || confirmed.Comment.ID != item.Comment.ID {
		return observedConflictResult(op, "update returned but the exact target could not be re-observed", item)
	}
	return observedPairConflictResult(op, fmt.Sprintf("update returned but canonical representation digest did not match: expected=%s current=%s",
		model.RepresentationDigest(body), model.RepresentationDigest(confirmed.Body)), item, confirmed)
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
		result := []Target{op.Target, *op.Desired.Peer}
		if authority := op.Precondition.RelationshipAuthority; authority != nil && authority.AssignmentProcess != nil {
			result = append(result, *authority.AssignmentProcess)
		}
		return result
	}
	result := []Target{op.Target}
	if authority := op.Precondition.RelationshipAuthority; authority != nil && authority.AssignmentProcess != nil {
		result = append(result, *authority.AssignmentProcess)
	}
	return result
}

func checkRelationshipOwnerAuthority(op Operation, carrier, assignmentProcess observed) error {
	authority := op.Precondition.RelationshipAuthority
	if authority == nil || authority.AssignmentProcess == nil || op.Precondition.AcceptedReceipt == nil {
		return errors.New("resolved relationship owner authority is missing")
	}
	if strings.TrimSpace(carrier.Comment.HTMLURL) != authority.CarrierURL ||
		strings.TrimSpace(assignmentProcess.Comment.HTMLURL) != authority.AssignmentProcessURL {
		return errors.New("resolved relationship provider URL does not match exact observation")
	}
	if model.RepresentationDigest(carrier.Body) != authority.CarrierBodyDigest ||
		model.RepresentationDigest(assignmentProcess.Body) != authority.AssignmentProcessBodyDigest {
		return errors.New("resolved relationship authority is stale")
	}
	workspace := model.ParseProcessWorkspace(authority.AssignmentProcess.ID, authority.AssignmentProcessURL, assignmentProcess.Body)
	if !workspace.Explicit || workspace.Blocking() || workspace.Workspace == nil || workspace.Workspace.Assignment == nil {
		return errors.New("resolved relationship assignment PROCESS is not canonical managed authority")
	}
	binding := workspace.Workspace.Assignment
	if binding.AssignmentID != authority.AssignmentID || binding.Digest != authority.AssignmentDigest ||
		binding.Generation != authority.AssignmentGeneration || binding.Role != op.Precondition.AcceptedReceipt.Role {
		return errors.New("resolved relationship assignment authority does not match projection")
	}
	return nil
}

func checkRelationshipAuthority(op Operation, carrier, peer, assignmentProcess observed) error {
	authority := op.Precondition.RelationshipAuthority
	if authority == nil || authority.AssignmentProcess == nil {
		return errors.New("resolved relationship authority is missing")
	}
	if strings.TrimSpace(carrier.Comment.HTMLURL) != authority.CarrierURL ||
		strings.TrimSpace(peer.Comment.HTMLURL) != authority.PeerURL ||
		strings.TrimSpace(assignmentProcess.Comment.HTMLURL) != authority.AssignmentProcessURL {
		return errors.New("resolved relationship provider URL does not match exact observation")
	}
	if model.RepresentationDigest(carrier.Body) != authority.CarrierBodyDigest ||
		model.RepresentationDigest(assignmentProcess.Body) != authority.AssignmentProcessBodyDigest {
		return errors.New("resolved relationship authority is stale")
	}
	workspace := model.ParseProcessWorkspace(authority.AssignmentProcess.ID, authority.AssignmentProcessURL, assignmentProcess.Body)
	if !workspace.Explicit || workspace.Blocking() || workspace.Workspace == nil || workspace.Workspace.Assignment == nil {
		return errors.New("resolved relationship assignment PROCESS is not canonical managed authority")
	}
	binding := workspace.Workspace.Assignment
	if binding.AssignmentID != authority.AssignmentID || binding.Digest != authority.AssignmentDigest ||
		binding.Generation != authority.AssignmentGeneration || binding.Role != op.Precondition.AcceptedReceipt.Role {
		return errors.New("resolved relationship assignment authority does not match projection")
	}
	return nil
}
func artifactURL(repo string, target Target, commentID int64) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-%d", repo, target.Issue, commentID)
}
func hasRelatedCommentLink(body string, comment github.Comment) bool {
	typed := model.ParseTypedComment(body)
	if len(typed.Errors) != 0 {
		return false
	}
	want := map[string]bool{}
	for _, candidate := range []string{comment.HTMLURL, comment.URL} {
		if normalized := model.NormalizeURL(candidate); normalized != "" {
			want[normalized] = true
		}
	}
	for _, related := range model.RelatedCommentURLs(typed) {
		if want[model.NormalizeURL(related)] {
			return true
		}
	}
	return false
}
func checkMutationPrecondition(op Operation, target Target, item observed) error {
	if endpoint, found := linkEndpointPrecondition(op, target); found {
		return checkRepresentationPrecondition(endpoint.RepresentationVersion, endpoint.BodyDigest, item)
	}
	if sameProjectionTarget(op.Target, target) {
		return checkRepresentationPrecondition(op.Precondition.RepresentationVersion, op.Precondition.BodyDigest, item)
	}
	return nil
}

func checkLinkEndpointPrecondition(op Operation, target Target, item observed, desired string, changed bool) error {
	endpoint, found := linkEndpointPrecondition(op, target)
	if !found {
		return nil
	}
	if !changed {
		if current := model.RepresentationDigest(item.Body); current != normalizeDigest(endpoint.AfterDigest) {
			return fmt.Errorf("satisfied endpoint digest conflict for %s: expected=%s current=%s",
				projectionTargetKey(target), normalizeDigest(endpoint.AfterDigest), current)
		}
		return nil
	}
	if err := checkRepresentationPrecondition(endpoint.RepresentationVersion, endpoint.BodyDigest, item); err != nil {
		return fmt.Errorf("endpoint %s: %w", projectionTargetKey(target), err)
	}
	if planned := model.RepresentationDigest(desired); planned != normalizeDigest(endpoint.AfterDigest) {
		return fmt.Errorf("endpoint %s after digest conflict: expected=%s planned=%s",
			projectionTargetKey(target), normalizeDigest(endpoint.AfterDigest), planned)
	}
	return nil
}

func linkEndpointPrecondition(op Operation, target Target) (EndpointPrecondition, bool) {
	for _, endpoint := range op.Precondition.Endpoints {
		if sameProjectionTarget(endpoint.Target, target) {
			return endpoint, true
		}
	}
	return EndpointPrecondition{}, false
}

func checkRepresentationPrecondition(version int64, digest string, item observed) error {
	if version > 0 && version != item.Version {
		return fmt.Errorf("representation conflict: expected=%d current=%d", version, item.Version)
	}
	if digest != "" && normalizeDigest(digest) != model.RepresentationDigest(item.Body) {
		return fmt.Errorf("body digest conflict: expected=%s current=%s", normalizeDigest(digest), model.RepresentationDigest(item.Body))
	}
	return nil
}

func checkAcceptedReceiptPrecondition(precondition Precondition, body string) error {
	expected := precondition.AcceptedReceipt
	if expected == nil {
		return nil
	}
	observed, found, err := model.ObserveAcceptedReceiptAuthority(body, expected.Role)
	if err != nil {
		return fmt.Errorf("observe accepted %s receipt authority: %w", expected.Role, err)
	}
	if !found {
		return fmt.Errorf("accepted %s receipt authority is missing from carrier", expected.Role)
	}
	if observed.ReceiptID != expected.ReceiptID || observed.Digest != expected.Digest ||
		observed.Generation != expected.Generation {
		return fmt.Errorf("accepted %s receipt authority does not match projection", expected.Role)
	}
	if expected.Role != assignment.RoleImplementation && expected.AssignmentID != "" && (observed.AssignmentID != expected.AssignmentID ||
		observed.AssignmentDigest != expected.AssignmentDigest) {
		return fmt.Errorf("accepted %s receipt assignment authority does not match projection", expected.Role)
	}
	if status := model.ParseTypedComment(body).Status; status != "done" {
		return fmt.Errorf("accepted %s receipt carrier must already have immutable done status, got %q", expected.Role, status)
	}
	return nil
}
func unchangedResult(op Operation, item observed, atomic bool) OperationResult {
	digest := model.RepresentationDigest(item.Body)
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "unchanged", Atomic: atomic,
		Guarantee: mutationGuarantee(atomic),
		CommentID: item.Comment.ID, URL: item.Comment.HTMLURL, BeforeDigest: digest, AfterDigest: digest}
}
func observedConflictResult(op Operation, message string, item observed) OperationResult {
	digest := model.RepresentationDigest(item.Body)
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "conflicted", Atomic: item.Version > 0,
		Guarantee: mutationGuarantee(item.Version > 0),
		CommentID: item.Comment.ID, URL: item.Comment.HTMLURL, BeforeDigest: digest, AfterDigest: digest, Message: message}
}
func observedFailureResult(op Operation, err error, atomic bool, item observed) OperationResult {
	result := failureResult(op, err, atomic)
	result.CommentID = item.Comment.ID
	result.URL = item.Comment.HTMLURL
	result.BeforeDigest = model.RepresentationDigest(item.Body)
	return result
}
func observedPairConflictResult(op Operation, message string, before, after observed) OperationResult {
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "conflicted", Atomic: after.Version > 0,
		Guarantee: mutationGuarantee(after.Version > 0),
		CommentID: after.Comment.ID, URL: after.Comment.HTMLURL,
		BeforeDigest: model.RepresentationDigest(before.Body), AfterDigest: model.RepresentationDigest(after.Body), Message: message}
}
func observedPairFailureResult(op Operation, err error, atomic bool, before, after observed) OperationResult {
	result := failureResult(op, err, atomic)
	result.CommentID = after.Comment.ID
	result.URL = after.Comment.HTMLURL
	result.BeforeDigest = model.RepresentationDigest(before.Body)
	result.AfterDigest = model.RepresentationDigest(after.Body)
	return result
}
func endpointResult(target Target, result OperationResult) EndpointResult {
	return EndpointResult{Target: target, CommentID: result.CommentID, URL: result.URL,
		BeforeDigest: result.BeforeDigest, AfterDigest: result.AfterDigest}
}
func conflictResult(op Operation, message string) OperationResult {
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: "conflicted", Atomic: false,
		Guarantee: github.CommentMutationNonAtomicSingleWriter, Message: message}
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
	return OperationResult{ID: op.ID, Kind: op.Kind, Status: status, Atomic: atomic,
		Guarantee: mutationGuarantee(atomic), Message: err.Error()}
}
func mutationGuarantee(atomic bool) github.CommentMutationGuarantee {
	if atomic {
		return github.CommentMutationStrictConditional
	}
	return github.CommentMutationNonAtomicSingleWriter
}
func retryable(err error) bool {
	var api *github.APIError
	if errors.As(err, &api) && (api.StatusCode == http.StatusTooManyRequests || api.StatusCode >= 500) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "rate limit") || strings.Contains(text, "timeout") || strings.Contains(text, "temporary")
}
