package github

import (
	"context"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
)

func TestNativeMergeAuthorityAdapterConforms(t *testing.T) {
	request, snapshot := githubAuthorityFixture()
	backend := &fakeNativeMergeAuthority{snapshot: snapshot}
	provider, err := NewMergeAuthorityProvider(MergeAuthorityProviderOptions{ProviderKey: "github",
		ProviderBuildIdentity: "github-native@sha256:0123", PrincipalMappingIdentity: "operators:test:v1",
		PrincipalMappings: []codereview.PrincipalMapping{
			{Provider: "github", StableID: "user:7", Principal: codereview.PrincipalIdentity{Realm: "people.example", StableID: "person:7"}},
			{Provider: "github", StableID: "user:9", Principal: codereview.PrincipalIdentity{Realm: "people.example", StableID: "person:9"}},
		}}, backend)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codereview.FetchMergeSnapshot(t.Context(), provider, request)
	if err != nil || got.AuthorityToken != snapshot.AuthorityToken {
		t.Fatalf("FetchMergeSnapshot() = %+v, %v", got, err)
	}
	result, err := codereview.MergeChange(t.Context(), provider, codereview.ConditionalMergeRequest{Reference: request.Reference,
		ExpectedHead: request.ExpectedSubjectRevision, AuthorityToken: snapshot.AuthorityToken})
	if err != nil || result.MergeID != "merge:42" || !backend.merged {
		t.Fatalf("MergeChange() = %+v, %v", result, err)
	}
}

func githubAuthorityFixture() (codereview.MergeSnapshotRequest, codereview.MergeSnapshot) {
	reference := codereview.Reference{ProviderKey: "github", ExternalRepository: "acme/widgets", ChangeID: "42"}
	check := codereview.CheckIdentity{Provider: "github", Key: "app:42/context:unit", Owner: "app:42", DisplayName: "unit"}
	request := codereview.MergeSnapshotRequest{Reference: reference, ExpectedSubjectRevision: "abc123", RequiredChecks: []codereview.CheckIdentity{check}}
	author := codereview.ActorIdentity{Provider: "github", StableID: "user:7", Kind: codereview.ActorHuman}
	reviewer := codereview.ActorIdentity{Provider: "github", StableID: "user:9", Kind: codereview.ActorHuman}
	snapshot := codereview.MergeSnapshot{ProtocolVersion: codereview.ProtocolVersion,
		SemanticGeneration: codereview.MergeAuthorityGeneration, ProviderBuildIdentity: "github-native@sha256:0123",
		Reference: reference, SubjectRevision: "abc123", ChangeState: codereview.ChangeOpen, CapturedAt: time.Now().UTC(),
		Review: codereview.ReviewAuthority{Mode: codereview.ReviewProviderNative, AuthorSetComplete: true,
			Authors: []codereview.ActorIdentity{author}, Policy: codereview.ReviewPolicy{RequiredApprovalCount: 1},
			Decisions: []codereview.ReviewDecision{{ID: "review:9", SubjectRevision: "abc123", Reviewer: reviewer,
				Verdict: codereview.ReviewApproved, ObservationID: "observation:9"}}, Findings: []codereview.ReviewFinding{},
			UnresolvedConversations: []string{}},
		Checks: []codereview.CheckConclusion{{Identity: check, SubjectRevision: "abc123", CurrentAttemptID: "run:3",
			ConfigurationGeneration: "ruleset:2", Conclusion: codereview.CheckSuccess}}, AuthorityToken: "github-authority-token:17"}
	return request, snapshot
}

type fakeNativeMergeAuthority struct {
	snapshot codereview.MergeSnapshot
	merged   bool
}

func (f *fakeNativeMergeAuthority) CollectMergeAuthority(context.Context, codereview.MergeSnapshotRequest) (codereview.MergeSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeNativeMergeAuthority) MergeWithAuthority(_ context.Context, request codereview.ConditionalMergeRequest) (codereview.ConditionalMergeResult, error) {
	f.merged = true
	return codereview.ConditionalMergeResult{Reference: request.Reference, ExpectedHead: request.ExpectedHead,
		MergeID: "merge:42", MergedRevision: "merge789", CanonicalURL: "https://github.com/acme/widgets/pull/42"}, nil
}
