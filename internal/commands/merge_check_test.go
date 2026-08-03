package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/mergecheck"
)

func TestMergeCheckSuccessAndFailurePerformZeroWrites(t *testing.T) {
	for _, test := range []struct {
		name       string
		conclusion codereview.CheckConclusionValue
		wantCode   int
	}{
		{"ready", codereview.CheckSuccess, 0},
		{"blocked", codereview.CheckFailure, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			withMergeWorkflow(t)
			backend := &mergeCommandBackend{issueState: "open"}
			provider := newMergeCommandProvider(test.conclusion, false)
			app, out, errOut := mergeCommandApp(t, backend, provider)
			code := app.runMergeCheck(t.Context(), []string{"--repo", "o/r", "--issue", "1", "--pr", "7", "--head", "head:2", "--json"})
			if code != test.wantCode || backend.issueWrites != 0 || backend.prWrites != 0 || provider.merges != 0 {
				t.Fatalf("exit=%d writes=%d/%d/%d stdout=%q stderr=%q", code, backend.issueWrites, backend.prWrites, provider.merges, out.String(), errOut.String())
			}
			if !strings.Contains(out.String(), `"schema_version": "issue-spec.merge-check/v1"`) {
				t.Fatalf("stdout = %q", out.String())
			}
		})
	}
}

func TestCommandScopeAuthorityValidatesOnlySelectedChain(t *testing.T) {
	backend := &mergeCommandBackend{issueState: "open", issues: map[int]github.Issue{
		10: {Number: 10, Body: "<!-- issue-spec:issue=proposal change=bounded version=1 -->"},
		11: {Number: 11, Body: "<!-- issue-spec:issue=design change=bounded version=1 -->\n- Proposal Issue: 10"},
		12: {Number: 12, Body: "<!-- issue-spec:issue=implement change=bounded version=1 -->\n- Design Issue: 11"},
		99: {Number: 99, Body: "unrelated"},
	}}
	proposal, design, implement := 10, 11, 12
	authority := &commandScopeAuthority{backend: backend, repo: "o/r"}
	if err := authority.Validate(t.Context(), mustMergeScope(t, "", "10", "11", "12")); err != nil {
		t.Fatal(err)
	}
	if backend.issueReads[99] != 0 || backend.issueReads[proposal] != 1 || backend.issueReads[design] != 1 || backend.issueReads[implement] != 1 {
		t.Fatalf("issue reads = %+v", backend.issueReads)
	}
}

func mustMergeScope(t *testing.T, issue, proposal, design, implement string) (scope mergecheck.ChangeScope) {
	t.Helper()
	scope, err := parseMergeChangeScope(issue, proposal, design, implement)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func withMergeWorkflow(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "schema: issue-spec\nexternal_code:\n  provider_key: code.example\n  merge:\n    required_checks:\n      - source: provider\n        provider: code.example\n        key: app:7/context:unit\n        owner: app:7\n        display_name: unit\n"
	if err := os.WriteFile(filepath.Join(root, "issue-spec", "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
}

func mergeCommandApp(t *testing.T, backend github.Backend, provider *mergeCommandProvider) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	application := newApp(strings.NewReader(""), &out, &errOut)
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.lookupOperatorProvider = func(context.Context, auth.Profile, string) (codereview.Provider, error) { return provider, nil }
	return application, &out, &errOut
}

type mergeCommandBackend struct {
	fakeGitHubBackend
	issueState  string
	issues      map[int]github.Issue
	issueReads  map[int]int
	issueWrites int
	prWrites    int
}

func (b *mergeCommandBackend) GetIssue(_ context.Context, _ string, number int) (github.Issue, error) {
	if b.issueReads == nil {
		b.issueReads = map[int]int{}
	}
	b.issueReads[number]++
	if issue, ok := b.issues[number]; ok {
		return issue, nil
	}
	if number != 1 {
		return github.Issue{}, errors.New("missing issue")
	}
	return github.Issue{Number: 1, State: b.issueState, Body: "simple change contract"}, nil
}
func (b *mergeCommandBackend) UpdateIssue(_ context.Context, _ string, number int, options github.UpdateIssueOptions) (github.Issue, error) {
	b.issueWrites++
	state := b.issueState
	if options.State != nil {
		state = *options.State
		b.issueState = state
	}
	return github.Issue{Number: number, State: state}, nil
}
func (b *mergeCommandBackend) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	pr := github.PullRequest{Number: 7}
	pr.Head.SHA = "head:2"
	return pr, nil
}
func (b *mergeCommandBackend) UpdatePullRequest(context.Context, string, int, github.UpdatePullRequestOptions) (github.PullRequest, error) {
	b.prWrites++
	return github.PullRequest{}, errors.New("unexpected PR write")
}

type mergeCommandProvider struct {
	conclusion codereview.CheckConclusionValue
	merged     bool
	snapshots  int
	merges     int
	mergeErr   error
}

func newMergeCommandProvider(conclusion codereview.CheckConclusionValue, merged bool) *mergeCommandProvider {
	return &mergeCommandProvider{conclusion: conclusion, merged: merged}
}
func (p *mergeCommandProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	return codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion, SemanticGeneration: codereview.MergeAuthorityGeneration,
		ProviderBuildIdentity: "bridge@sha256:1234", Values: codereview.RequiredMergeAuthorityCapabilities()}, nil
}
func (*mergeCommandProvider) CanonicalPrincipalMappingSource() string { return "operators:test:v1" }
func (*mergeCommandProvider) Snapshot(context.Context, codereview.SnapshotRequest) (codereview.Snapshot, error) {
	return codereview.Snapshot{}, errors.New("legacy evidence snapshot must not be called")
}
func (p *mergeCommandProvider) MergeSnapshot(_ context.Context, request codereview.MergeSnapshotRequest) (codereview.MergeSnapshot, error) {
	p.snapshots++
	author := mergeCommandActor("user:1", "person:1")
	reviewer := mergeCommandActor("user:2", "person:2")
	state := codereview.ChangeOpen
	if p.merged {
		state = codereview.ChangeMerged
	}
	return codereview.MergeSnapshot{ProtocolVersion: codereview.ProtocolVersion, SemanticGeneration: codereview.MergeAuthorityGeneration,
		ProviderBuildIdentity: "bridge@sha256:1234", Reference: request.Reference, SubjectRevision: request.ExpectedSubjectRevision,
		ChangeState: state, CapturedAt: time.Now().UTC(), Review: codereview.ReviewAuthority{Mode: codereview.ReviewProviderNative,
			AuthorSetComplete: true, Authors: []codereview.ActorIdentity{author}, Policy: codereview.ReviewPolicy{RequiredApprovalCount: 1},
			Decisions: []codereview.ReviewDecision{{ID: "review:2", SubjectRevision: request.ExpectedSubjectRevision, Reviewer: reviewer,
				Verdict: codereview.ReviewApproved, ObservationID: "observation:2"}}, Findings: []codereview.ReviewFinding{}, UnresolvedConversations: []string{}},
		Checks: []codereview.CheckConclusion{{Identity: request.RequiredChecks[0], SubjectRevision: request.ExpectedSubjectRevision,
			CurrentAttemptID: "run:3", ConfigurationGeneration: "ruleset:2", Conclusion: p.conclusion}}, AuthorityToken: "token:fresh"}, nil
}
func (p *mergeCommandProvider) MergeChange(_ context.Context, request codereview.ConditionalMergeRequest) (codereview.ConditionalMergeResult, error) {
	p.merges++
	if p.mergeErr != nil {
		return codereview.ConditionalMergeResult{}, p.mergeErr
	}
	p.merged = true
	return codereview.ConditionalMergeResult{Reference: request.Reference, ExpectedHead: request.ExpectedHead,
		MergeID: "merge:7", MergedRevision: "merge:9", CanonicalURL: "https://code.example/changes/7"}, nil
}

func mergeCommandActor(source, principal string) codereview.ActorIdentity {
	return codereview.ActorIdentity{Provider: "code.example", StableID: source, Kind: codereview.ActorHuman,
		CanonicalPrincipal: codereview.PrincipalIdentity{Realm: "people.example", StableID: principal}}
}
