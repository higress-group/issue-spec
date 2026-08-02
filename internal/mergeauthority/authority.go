// Package mergeauthority owns all I/O used by merge readiness and merge.
// Callers cannot provide a saved decision, digest, authority token, or fallback
// fact to Merge; every authoritative value is collected again inside it.
package mergeauthority

import (
	"context"
	"errors"
	"fmt"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/mergecheck"
)

var (
	ErrInvalidRequest        = errors.New("merge authority request is invalid")
	ErrNotReady              = errors.New("change is not ready to merge")
	ErrMergedStateUnobserved = errors.New("merged state could not be freshly observed")
)

type Request struct {
	Scope          mergecheck.ChangeScope     `json:"scope"`
	Reference      codereview.Reference       `json:"reference"`
	ExpectedHead   string                     `json:"expected_head"`
	RequiredChecks []codereview.CheckIdentity `json:"required_checks"`
}

func (r Request) Validate() error {
	providerRequest := codereview.MergeSnapshotRequest{Reference: r.Reference,
		ExpectedSubjectRevision: r.ExpectedHead, RequiredChecks: r.RequiredChecks}
	if r.Scope.Validate() != nil || providerRequest.Validate() != nil {
		return ErrInvalidRequest
	}
	return nil
}

// ScopeAuthority reads only the selected contract/predecessor chain and owns
// idempotent post-merge reconciliation for that exact set.
type ScopeAuthority interface {
	Validate(context.Context, mergecheck.ChangeScope) error
	Reconcile(context.Context, mergecheck.ChangeScope, codereview.MergeSnapshot) (Reconciliation, error)
}

type ReconciledIssue struct {
	Issue         int  `json:"issue"`
	Closed        bool `json:"closed"`
	AlreadyClosed bool `json:"already_closed"`
}

type Reconciliation struct {
	Issues []ReconciledIssue `json:"issues"`
}

type CheckResult struct {
	Decision              mergecheck.Decision `json:"decision"`
	ProviderBuildIdentity string              `json:"provider_build_identity"`
	CapturedAt            string              `json:"captured_at"`
}

type MergeResult struct {
	Decision       mergecheck.Decision                `json:"decision"`
	Merge          *codereview.ConditionalMergeResult `json:"merge,omitempty"`
	AlreadyMerged  bool                               `json:"already_merged,omitempty"`
	Reconciliation *Reconciliation                    `json:"reconciliation,omitempty"`
	PostMergeError string                             `json:"post_merge_error,omitempty"`
}

// PostMergeError preserves the successful provider result while making failed
// observation or closure reconciliation actionable to the caller.
type PostMergeError struct {
	Operation string
	Err       error
}

func (e *PostMergeError) Error() string { return fmt.Sprintf("post-merge %s: %v", e.Operation, e.Err) }
func (e *PostMergeError) Unwrap() error { return e.Err }

type Engine struct {
	provider codereview.MergeAuthorityProvider
	scope    ScopeAuthority
}

func New(provider codereview.MergeAuthorityProvider, scope ScopeAuthority) (*Engine, error) {
	if provider == nil || scope == nil {
		return nil, ErrInvalidRequest
	}
	return &Engine{provider: provider, scope: scope}, nil
}

// Check collects one fresh generation and returns only a diagnostic decision;
// the opaque token is intentionally not exposed.
func (e *Engine) Check(ctx context.Context, request Request) (CheckResult, error) {
	collection, err := e.collect(ctx, request)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Decision: collection.decision, ProviderBuildIdentity: collection.snapshot.ProviderBuildIdentity,
		CapturedAt: collection.snapshot.CapturedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}, nil
}

// Merge performs a new collection and evaluation in this invocation. Its API
// has no decision/token parameters, making saved merge-check output unusable
// as proof. The provider mutation remains the sole pre-reconciliation write.
func (e *Engine) Merge(ctx context.Context, request Request) (MergeResult, error) {
	collection, err := e.collect(ctx, request)
	if err != nil {
		return MergeResult{}, err
	}
	result := MergeResult{Decision: collection.decision}
	if collection.snapshot.ChangeState == codereview.ChangeMerged {
		result.AlreadyMerged = true
		return e.reconcileObserved(ctx, request.Scope, collection.snapshot, result)
	}
	if !collection.decision.Ready {
		return result, ErrNotReady
	}
	capabilities, err := codereview.RequireMergeAuthorityCapabilities(ctx, e.provider)
	if err != nil {
		return result, err
	}
	if capabilities.ProviderBuildIdentity != collection.snapshot.ProviderBuildIdentity {
		return result, errors.New("merge provider build changed after authority collection")
	}
	merged, err := codereview.MergeChange(ctx, e.provider, codereview.ConditionalMergeRequest{
		Reference: request.Reference, ExpectedHead: request.ExpectedHead, AuthorityToken: collection.snapshot.AuthorityToken,
		ExternalAuthorityGeneration: collection.snapshot.ExternalAuthorityGeneration,
	})
	if err != nil {
		// A transport failure may hide a committed merge. Re-observe before
		// returning, without replaying the write or fabricating merge data.
		if observed, observeErr := codereview.FetchMergeSnapshot(ctx, e.provider, providerRequest(request)); observeErr == nil && observed.ChangeState == codereview.ChangeMerged {
			result.AlreadyMerged = true
			return e.reconcileObserved(ctx, request.Scope, observed, result)
		}
		return result, err
	}
	result.Merge = &merged

	// The merge response is not used as a closure oracle. Re-read the exact
	// subject and require the provider to report merged state before touching
	// the issue backend.
	observed, err := codereview.FetchMergeSnapshot(ctx, e.provider, providerRequest(request))
	if err != nil || observed.ChangeState != codereview.ChangeMerged {
		if err == nil {
			err = fmt.Errorf("provider reports state %q", observed.ChangeState)
		}
		result.PostMergeError = err.Error()
		return result, &PostMergeError{Operation: "observation", Err: fmt.Errorf("%w: %v", ErrMergedStateUnobserved, err)}
	}
	return e.reconcileObserved(ctx, request.Scope, observed, result)
}

func (e *Engine) reconcileObserved(ctx context.Context, scope mergecheck.ChangeScope, observed codereview.MergeSnapshot,
	result MergeResult) (MergeResult, error) {
	reconciliation, err := e.scope.Reconcile(ctx, scope, observed)
	result.Reconciliation = &reconciliation
	if err != nil {
		result.PostMergeError = err.Error()
		return result, &PostMergeError{Operation: "reconciliation", Err: err}
	}
	return result, nil
}

type collection struct {
	snapshot codereview.MergeSnapshot
	decision mergecheck.Decision
}

func (e *Engine) collect(ctx context.Context, request Request) (collection, error) {
	if err := request.Validate(); err != nil {
		return collection{}, err
	}
	if err := e.scope.Validate(ctx, request.Scope); err != nil {
		return collection{}, fmt.Errorf("validate selected change scope: %w", err)
	}
	snapshot, err := codereview.FetchMergeSnapshot(ctx, e.provider, providerRequest(request))
	if err != nil {
		return collection{}, fmt.Errorf("collect fresh merge authority: %w", err)
	}
	input := mergecheck.Input{Scope: request.Scope,
		Subject:  mergecheck.CodeSubject{Reference: snapshot.Reference, Revision: snapshot.SubjectRevision, State: snapshot.ChangeState},
		Required: append([]codereview.CheckIdentity(nil), request.RequiredChecks...),
		Checks:   append([]codereview.CheckConclusion(nil), snapshot.Checks...), Review: snapshot.Review,
		ProviderGate: []mergecheck.ProviderPolicyObservation{},
	}
	return collection{snapshot: snapshot, decision: mergecheck.Evaluate(input)}, nil
}

func providerRequest(request Request) codereview.MergeSnapshotRequest {
	return codereview.MergeSnapshotRequest{Reference: request.Reference, ExpectedSubjectRevision: request.ExpectedHead,
		RequiredChecks: append([]codereview.CheckIdentity(nil), request.RequiredChecks...)}
}
