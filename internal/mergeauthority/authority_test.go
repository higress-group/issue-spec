package mergeauthority

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/mergecheck"
)

func TestCheckIsReadOnlyAndMergeRecollectsThenReconciles(t *testing.T) {
	request, snapshots := authorityFixture()
	provider := &fakeProvider{snapshots: snapshots}
	scope := &fakeScope{}
	engine, err := New(provider, scope)
	if err != nil {
		t.Fatal(err)
	}

	checked, err := engine.Check(t.Context(), request)
	if err != nil || !checked.Decision.Ready || checked.Decision.SnapshotDigest == "" {
		t.Fatalf("Check() = %+v, %v", checked, err)
	}
	if provider.merges != 0 || scope.reconciles != 0 {
		t.Fatalf("read-only check wrote: merges=%d reconciliation=%d", provider.merges, scope.reconciles)
	}

	merged, err := engine.Merge(t.Context(), request)
	if err != nil || merged.Merge == nil || merged.Reconciliation == nil {
		t.Fatalf("Merge() = %+v, %v", merged, err)
	}
	if provider.snapshotsRead != 3 || provider.merges != 1 || scope.reconciles != 1 || provider.lastToken != "token:fresh" {
		t.Fatalf("calls snapshots=%d merges=%d reconciles=%d token=%q", provider.snapshotsRead, provider.merges, scope.reconciles, provider.lastToken)
	}
}

func TestMergeFailsClosedForHeadOrSameHeadAuthorityDrift(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"head", errors.New("head moved")}, {"policy", errors.New("authority token changed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, snapshots := authorityFixture()
			provider := &fakeProvider{snapshots: []codereview.MergeSnapshot{snapshots[1], snapshots[1]}, mergeErr: test.err}
			scope := &fakeScope{}
			engine, _ := New(provider, scope)
			result, err := engine.Merge(t.Context(), request)
			if !errors.Is(err, test.err) || result.Merge != nil || provider.merges != 1 || scope.reconciles != 0 {
				t.Fatalf("Merge() = %+v, %v calls=%d/%d", result, err, provider.merges, scope.reconciles)
			}
		})
	}
}

func TestMergePreservesSuccessWhenPostMergeObservationFails(t *testing.T) {
	request, snapshots := authorityFixture()
	snapshots = snapshots[1:2]
	provider := &fakeProvider{snapshots: snapshots}
	scope := &fakeScope{}
	engine, _ := New(provider, scope)
	result, err := engine.Merge(t.Context(), request)
	var post *PostMergeError
	if !errors.As(err, &post) || result.Merge == nil || result.PostMergeError == "" || scope.reconciles != 0 {
		t.Fatalf("Merge() = %+v, %v", result, err)
	}
}

func TestMergeRetryObservesCommittedStateWithoutReplayingMutation(t *testing.T) {
	request, snapshots := authorityFixture()
	provider := &fakeProvider{snapshots: snapshots[2:]}
	scope := &fakeScope{}
	engine, _ := New(provider, scope)
	result, err := engine.Merge(t.Context(), request)
	if err != nil || !result.AlreadyMerged || result.Merge != nil || provider.merges != 0 || scope.reconciles != 1 {
		t.Fatalf("Merge() = %+v, %v calls=%d/%d", result, err, provider.merges, scope.reconciles)
	}
}

func authorityFixture() (Request, []codereview.MergeSnapshot) {
	root := 10
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	check := codereview.CheckIdentity{Provider: "code.example", Key: "app:7/context:unit", Owner: "app:7", DisplayName: "unit"}
	request := Request{Scope: mergecheck.ChangeScope{ProposalIssue: &root}, Reference: reference,
		ExpectedHead: "head:2", RequiredChecks: []codereview.CheckIdentity{check}}
	author := testActor("user:1", "person:1")
	reviewer := testActor("user:2", "person:2")
	base := codereview.MergeSnapshot{ProtocolVersion: codereview.ProtocolVersion, SemanticGeneration: codereview.MergeAuthorityGeneration,
		ProviderBuildIdentity: "bridge@sha256:1234", Reference: reference, SubjectRevision: "head:2", ChangeState: codereview.ChangeOpen,
		CapturedAt: time.Now().UTC(), Review: codereview.ReviewAuthority{Mode: codereview.ReviewProviderNative,
			AuthorSetComplete: true, Authors: []codereview.ActorIdentity{author}, Policy: codereview.ReviewPolicy{RequiredApprovalCount: 1},
			Decisions: []codereview.ReviewDecision{{ID: "review:2", SubjectRevision: "head:2", Reviewer: reviewer,
				Verdict: codereview.ReviewApproved, ObservationID: "observation:2"}}, Findings: []codereview.ReviewFinding{}, UnresolvedConversations: []string{}},
		Checks: []codereview.CheckConclusion{{Identity: check, SubjectRevision: "head:2", CurrentAttemptID: "run:3",
			ConfigurationGeneration: "ruleset:2", Conclusion: codereview.CheckSuccess}}, AuthorityToken: "token:check"}
	fresh := base
	fresh.AuthorityToken = "token:fresh"
	merged := fresh
	merged.ChangeState = codereview.ChangeMerged
	merged.AuthorityToken = "token:observed-merged"
	return request, []codereview.MergeSnapshot{base, fresh, merged}
}

func testActor(source, principal string) codereview.ActorIdentity {
	return codereview.ActorIdentity{Provider: "code.example", StableID: source, Kind: codereview.ActorHuman,
		CanonicalPrincipal: codereview.PrincipalIdentity{Realm: "people.example", StableID: principal}}
}

type fakeProvider struct {
	snapshots     []codereview.MergeSnapshot
	snapshotsRead int
	merges        int
	lastToken     string
	mergeErr      error
}

func (f *fakeProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion, SemanticGeneration: codereview.MergeAuthorityGeneration,
		ProviderBuildIdentity: "bridge@sha256:1234", Values: codereview.RequiredMergeAuthorityCapabilities()}, nil
}

func (*fakeProvider) CanonicalPrincipalMappingSource() string { return "operators:test:v1" }
func (f *fakeProvider) MergeSnapshot(context.Context, codereview.MergeSnapshotRequest) (codereview.MergeSnapshot, error) {
	if f.snapshotsRead >= len(f.snapshots) {
		return codereview.MergeSnapshot{}, errors.New("no snapshot")
	}
	result := f.snapshots[f.snapshotsRead]
	f.snapshotsRead++
	return result, nil
}
func (f *fakeProvider) MergeChange(_ context.Context, request codereview.ConditionalMergeRequest) (codereview.ConditionalMergeResult, error) {
	f.merges++
	f.lastToken = request.AuthorityToken
	if f.mergeErr != nil {
		return codereview.ConditionalMergeResult{}, f.mergeErr
	}
	return codereview.ConditionalMergeResult{Reference: request.Reference, ExpectedHead: request.ExpectedHead,
		MergeID: "merge:42", MergedRevision: "merge:99", CanonicalURL: "https://code.example/changes/42"}, nil
}

type fakeScope struct{ reconciles int }

func (f *fakeScope) Validate(context.Context, mergecheck.ChangeScope) error { return nil }
func (f *fakeScope) Reconcile(_ context.Context, scope mergecheck.ChangeScope, observed codereview.MergeSnapshot) (Reconciliation, error) {
	f.reconciles++
	if observed.ChangeState != codereview.ChangeMerged {
		return Reconciliation{}, errors.New("provider state is not merged")
	}
	result := Reconciliation{}
	for _, issue := range scope.IssueNumbers() {
		result.Issues = append(result.Issues, ReconciledIssue{Issue: issue, Closed: true})
	}
	return result, nil
}
