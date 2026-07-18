package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestCodeChangeAttachHelpAndRefreshValidation(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runCodeChange(t.Context(), []string{"attach", "--help"}); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	for _, flag := range []string{"--repo", "--implement", "--change-id", "--revision", "--refresh", "--expected-version", "--json", "--hostname"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("help is missing %s:\n%s", flag, out.String())
		}
	}
	for _, forbidden := range []string{"--provider", "--external-repository", "--canonical-url"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatalf("help exposes caller-controlled authority flag %s:\n%s", forbidden, out.String())
		}
	}

	base := []string{"attach", "--repo", "acme/widgets", "--implement", "9", "--change-id", "42", "--revision", "head-abc"}
	for _, extra := range [][]string{{"--refresh"}, {"--expected-version", "4"}, {"--refresh", "--expected-version", "0"}} {
		out.Reset()
		errOut.Reset()
		if code := app.runCodeChange(t.Context(), append(append([]string(nil), base...), extra...)); code != 2 ||
			!strings.Contains(errOut.String(), "--refresh and a positive --expected-version") {
			t.Fatalf("args=%v exit=%d stderr=%q", extra, code, errOut.String())
		}
	}
}

func TestCodeChangeAttachUsesBindingAuthorityAndExactRetry(t *testing.T) {
	profile := setupCodeChangeProfile(t)
	backend := newFakeCodeChangeBackend()
	provider := newFakeNavigationProvider()
	app, out, errOut := setupCodeChangeApp(t, profile, backend, provider)
	args := []string{"attach", "--repo", "acme/widgets", "--implement", "9", "--change-id", "change-42",
		"--revision", "head-abc", "--json"}

	var first codeChangeAttachResult
	for attempt := 0; attempt < 2; attempt++ {
		out.Reset()
		errOut.Reset()
		if code := app.runCodeChange(t.Context(), args); code != 0 || errOut.Len() != 0 {
			t.Fatalf("attempt=%d exit=%d stdout=%q stderr=%q", attempt+1, code, out.String(), errOut.String())
		}
		var result codeChangeAttachResult
		decodeCommandJSON(t, out.Bytes(), &result)
		if attempt == 0 {
			first = result
		} else if result != first {
			t.Fatalf("exact retry changed result: first=%+v second=%+v", first, result)
		}
	}

	if backend.resolveRepo != "acme/widgets" || backend.resolveIssue != 9 || len(backend.inputs) != 2 {
		t.Fatalf("resolve repo=%q issue=%d inputs=%d", backend.resolveRepo, backend.resolveIssue, len(backend.inputs))
	}
	if provider.snapshotCalls != 2 || provider.mutationCalls != 0 || len(provider.requests) != 2 {
		t.Fatalf("snapshot calls=%d mutation calls=%d requests=%d", provider.snapshotCalls, provider.mutationCalls, len(provider.requests))
	}
	wantReference := codereview.Reference{ProviderKey: backend.binding.ProviderKey,
		ExternalRepository: backend.binding.ExternalRepositoryID, ChangeID: "change-42"}
	for _, request := range provider.requests {
		if request.Reference != wantReference || request.SubjectRevision != "head-abc" {
			t.Fatalf("snapshot request = %+v, want binding authority %+v", request, wantReference)
		}
	}
	for _, input := range backend.inputs {
		if input.ProviderKey != wantReference.ProviderKey || input.ExternalRepositoryID != wantReference.ExternalRepository ||
			input.ExternalID != wantReference.ChangeID || input.RelationKind != "code_change" || input.LifecycleState != "active" ||
			input.Visibility != "repository" || input.Refresh || input.ExpectedVersion != nil ||
			string(input.Metadata) != `{"head_revision":"head-abc"}` {
			t.Fatalf("upsert input = %+v metadata=%s", input, input.Metadata)
		}
	}
	if !first.OK || first.Action != "attached" || first.ProviderKey != wantReference.ProviderKey ||
		first.ExternalRepository != wantReference.ExternalRepository || first.ChangeID != wantReference.ChangeID ||
		first.Revision != "head-abc" || first.RefreshRequested || first.RepresentationVersion != 1 {
		t.Fatalf("result = %+v", first)
	}
}

func TestCodeChangeAttachRefreshForwardsCASAndReturnsNewVersion(t *testing.T) {
	profile := setupCodeChangeProfile(t)
	backend := newFakeCodeChangeBackend()
	backend.upsert = func(input github.NativeUpsertReferenceInput) (github.NativeReference, error) {
		return backend.reference(input, 5), nil
	}
	provider := newFakeNavigationProvider()
	app, out, errOut := setupCodeChangeApp(t, profile, backend, provider)
	code := app.runCodeChange(t.Context(), []string{"attach", "--repo", "acme/widgets", "--implement", "9",
		"--change-id", "change-42", "--revision", "head-def", "--refresh", "--expected-version", "4", "--json"})
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if len(backend.inputs) != 1 || !backend.inputs[0].Refresh || backend.inputs[0].ExpectedVersion == nil ||
		*backend.inputs[0].ExpectedVersion != 4 || string(backend.inputs[0].Metadata) != `{"head_revision":"head-def"}` {
		t.Fatalf("refresh input = %+v", backend.inputs)
	}
	var result codeChangeAttachResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if !result.RefreshRequested || result.RepresentationVersion != 5 || result.Revision != "head-def" {
		t.Fatalf("refresh result = %+v", result)
	}
}

func TestCodeChangeAttachFailsBeforeUpsertForBindingAndProviderProblems(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(*fakeCodeChangeBackend, *fakeNavigationProvider)
		wantCode string
		wantSnap int
	}{
		{
			name: "missing source binding",
			prepare: func(backend *fakeCodeChangeBackend, _ *fakeNavigationProvider) {
				backend.bindingErr = errors.New("active binding not found")
			},
			wantCode: "source_binding_unavailable",
		},
		{
			name: "missing snapshot capability",
			prepare: func(_ *fakeCodeChangeBackend, provider *fakeNavigationProvider) {
				provider.capabilities.Values = nil
			},
			wantCode: "provider_capability_missing",
		},
		{
			name: "invalid provider canonical URL",
			prepare: func(_ *fakeCodeChangeBackend, provider *fakeNavigationProvider) {
				provider.editSnapshot = func(snapshot *codereview.Snapshot) {
					snapshot.Facts[0].CanonicalURL += "?access_token=secret"
				}
			},
			wantCode: "provider_data_invalid",
			wantSnap: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := setupCodeChangeProfile(t)
			backend := newFakeCodeChangeBackend()
			provider := newFakeNavigationProvider()
			test.prepare(backend, provider)
			app, out, errOut := setupCodeChangeApp(t, profile, backend, provider)
			code := app.runCodeChange(t.Context(), []string{"attach", "--repo", "acme/widgets", "--implement", "9",
				"--change-id", "change-42", "--revision", "head-abc", "--json"})
			if code != 1 || errOut.Len() != 0 || len(backend.inputs) != 0 || provider.snapshotCalls != test.wantSnap {
				t.Fatalf("exit=%d inputs=%d snapshots=%d stdout=%q stderr=%q", code, len(backend.inputs), provider.snapshotCalls,
					out.String(), errOut.String())
			}
			var result codeChangeAttachErrorResult
			decodeCommandJSON(t, out.Bytes(), &result)
			if result.OK || result.Code != test.wantCode || result.Reason != "" || len(result.References) != 0 {
				t.Fatalf("error result = %+v", result)
			}
		})
	}
}

func TestCodeChangeAttachPreservesTypedStaleConflictJSON(t *testing.T) {
	profile := setupCodeChangeProfile(t)
	backend := newFakeCodeChangeBackend()
	identity := github.NativeReferenceIdentity{ID: uuid.NewString(), ProviderKey: backend.binding.ProviderKey,
		ExternalRepositoryID: backend.binding.ExternalRepositoryID, ExternalID: "change-42", RepresentationVersion: 7}
	backend.upsert = func(github.NativeUpsertReferenceInput) (github.NativeReference, error) {
		return github.NativeReference{}, &github.NativeCodeChangeConflictError{
			Reason: github.NativeCodeChangeConflictStaleReferenceVersion, References: []github.NativeReferenceIdentity{identity},
			RequestID: "request-123",
		}
	}
	app, out, errOut := setupCodeChangeApp(t, profile, backend, newFakeNavigationProvider())
	code := app.runCodeChange(t.Context(), []string{"attach", "--repo", "acme/widgets", "--implement", "9",
		"--change-id", "change-42", "--revision", "head-def", "--refresh", "--expected-version", "6", "--json"})
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result codeChangeAttachErrorResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.OK || result.Code != "code_change_conflict" || result.Reason != github.NativeCodeChangeConflictStaleReferenceVersion ||
		len(result.References) != 1 || result.References[0] != identity || result.RequestID != "request-123" {
		t.Fatalf("conflict result = %+v", result)
	}
}

func TestCodeChangeAttachDoesNotInventTypedConflictFromGenericError(t *testing.T) {
	profile := setupCodeChangeProfile(t)
	backend := newFakeCodeChangeBackend()
	backend.upsert = func(github.NativeUpsertReferenceInput) (github.NativeReference, error) {
		return github.NativeReference{}, errors.New("untyped conflict")
	}
	app, out, _ := setupCodeChangeApp(t, profile, backend, newFakeNavigationProvider())
	if code := app.runCodeChange(t.Context(), []string{"attach", "--repo", "acme/widgets", "--implement", "9",
		"--change-id", "change-42", "--revision", "head-abc", "--json"}); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	var result codeChangeAttachErrorResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Code != "reference_upsert_failed" || result.Reason != "" || len(result.References) != 0 || result.RequestID != "" {
		t.Fatalf("generic result = %+v", result)
	}
}

func TestCodeChangeAttachRejectsGitHubProfileBeforeBackendResolution(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	profile := auth.BuiltinGitHubProfile("github.com")
	profile.Name = "github-code-change"
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.profileName = profile.Name
	app.newNativeCodeChangeBackend = func(auth.Profile, string) (nativeCodeChangeBackend, error) {
		t.Fatal("GitHub profile reached native backend resolution")
		return nil, nil
	}
	code := app.runCodeChange(t.Context(), []string{"attach", "--repo", "acme/widgets", "--implement", "9",
		"--change-id", "change-42", "--revision", "head-abc", "--json"})
	if code != 1 || errOut.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result codeChangeAttachErrorResult
	decodeCommandJSON(t, out.Bytes(), &result)
	if result.Code != "self_hosted_required" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCommandNativeCodeChangeBackendResolvesImplementIssue(t *testing.T) {
	orgID, repoID, issueID := uuid.New(), uuid.New(), uuid.New()
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer attach-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(github.NativeContext{Organizations: []github.NativeOrganizationContext{{
				ID: orgID.String(), Name: "acme",
			}}})
		case "/api/v1/context/orgs/" + orgID.String() + "/repos":
			_ = json.NewEncoder(w).Encode(github.NativeRepositoriesContext{Repositories: []github.NativeRepositoryContext{{
				Repository: github.NativeRepositorySummary{ID: repoID.String(), OrganizationID: orgID.String(), Name: "widgets"},
			}}})
		case "/api/v3/repos/acme/widgets/issues/9":
			_ = json.NewEncoder(w).Encode(github.Issue{Number: 9,
				NodeID: base64.RawStdEncoding.EncodeToString([]byte("Issue:" + issueID.String())),
				Body:   "<!-- issue-spec:issue=implement change=third-party-provider version=1 -->\n"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	profile := auth.Profile{Kind: auth.ProfileKindHosted, Hostname: parsed.Host,
		APIURL: server.URL + "/api/v3", NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL}
	backend, err := defaultNewNativeCodeChangeBackend(profile, "attach-secret")
	if err != nil {
		t.Fatal(err)
	}
	scope, gotIssueID, err := backend.ResolveNativeIssue(t.Context(), "acme/widgets", 9)
	if err != nil {
		t.Fatal(err)
	}
	if scope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || gotIssueID != issueID {
		t.Fatalf("scope=%+v issue=%s", scope, gotIssueID)
	}
	wantRequests := []string{"GET /api/v1/context", "GET /api/v1/context/orgs/" + orgID.String() + "/repos",
		"GET /api/v3/repos/acme/widgets/issues/9"}
	if strings.Join(requests, "\n") != strings.Join(wantRequests, "\n") {
		t.Fatalf("requests = %v, want %v", requests, wantRequests)
	}
}

type fakeCodeChangeBackend struct {
	scope        models.RepoScope
	issueID      uuid.UUID
	binding      github.NativeBinding
	bindingErr   error
	references   []github.NativeReference
	referenceErr error
	issueBackend github.IssueBackend
	resolveRepo  string
	resolveIssue int
	inputs       []github.NativeUpsertReferenceInput
	upsert       func(github.NativeUpsertReferenceInput) (github.NativeReference, error)
}

func newFakeCodeChangeBackend() *fakeCodeChangeBackend {
	return &fakeCodeChangeBackend{
		scope:   models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()},
		issueID: uuid.New(),
		binding: github.NativeBinding{ID: uuid.NewString(), ProviderKey: "code.example",
			ExternalRepositoryID: "binding-owned/widgets", CloneURL: "ssh://code.example/binding-owned/widgets.git",
			WebURL: "https://code.example/binding-owned/widgets", DefaultBranch: "main", Version: 1, Active: true},
	}
}

func (b *fakeCodeChangeBackend) ResolveNativeIssue(_ context.Context, repository string, issueNumber int) (models.RepoScope, uuid.UUID, error) {
	b.resolveRepo = repository
	b.resolveIssue = issueNumber
	return b.scope, b.issueID, nil
}

func (b *fakeCodeChangeBackend) GetNativeActiveBinding(context.Context, models.RepoScope) (github.NativeBinding, error) {
	return b.binding, b.bindingErr
}

func (b *fakeCodeChangeBackend) ListNativeReferences(context.Context, models.RepoScope, uuid.UUID) ([]github.NativeReference, error) {
	return append([]github.NativeReference(nil), b.references...), b.referenceErr
}

func (b *fakeCodeChangeBackend) UpsertNativeReference(_ context.Context, _ models.RepoScope, _ uuid.UUID,
	input github.NativeUpsertReferenceInput) (github.NativeReference, error) {
	b.inputs = append(b.inputs, input)
	if b.upsert != nil {
		return b.upsert(input)
	}
	return b.reference(input, 1), nil
}

func (b *fakeCodeChangeBackend) CompatibilityIssueBackend() github.IssueBackend { return b.issueBackend }

func (b *fakeCodeChangeBackend) reference(input github.NativeUpsertReferenceInput, version int64) github.NativeReference {
	return github.NativeReference{ID: "d50bce8e-28a9-40e4-9bcb-9eb245628f43", IssueID: b.issueID.String(),
		ProviderKey: input.ProviderKey, RelationKind: input.RelationKind, ExternalRepositoryID: input.ExternalRepositoryID,
		ExternalID: input.ExternalID, CanonicalURL: input.CanonicalURL, LifecycleState: input.LifecycleState,
		Visibility: input.Visibility, Metadata: input.Metadata, RepresentationVersion: version}
}

type fakeNavigationProvider struct {
	capabilities  codereview.Capabilities
	requests      []codereview.SnapshotRequest
	snapshotCalls int
	mutationCalls int
	editSnapshot  func(*codereview.Snapshot)
}

func newFakeNavigationProvider() *fakeNavigationProvider {
	return &fakeNavigationProvider{capabilities: codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
		Values: []codereview.Capability{codereview.CapabilityEvidenceSnapshot}}}
}

func (p *fakeNavigationProvider) Capabilities(context.Context) (codereview.Capabilities, error) {
	return p.capabilities, nil
}

func (p *fakeNavigationProvider) Snapshot(_ context.Context, request codereview.SnapshotRequest) (codereview.Snapshot, error) {
	p.snapshotCalls++
	p.requests = append(p.requests, request)
	observed := time.Date(2026, 7, 17, 1, 2, 2, 0, time.UTC)
	snapshot := codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: request.Reference,
		SubjectRevision: request.SubjectRevision, CapturedAt: observed.Add(time.Second), Facts: []codereview.ProviderFact{{
			ID: "change-1", Kind: codereview.EvidenceChange, ExternalID: request.Reference.ChangeID, State: "open",
			SubjectRevision: request.SubjectRevision, ObservedAt: observed,
			CanonicalURL:  "https://code.example/binding-owned/widgets/change/" + request.Reference.ChangeID,
			PayloadDigest: strings.Repeat("a", 64),
		}}}
	if p.editSnapshot != nil {
		p.editSnapshot(&snapshot)
	}
	return snapshot, nil
}

func (p *fakeNavigationProvider) Mutate(context.Context, codereview.MutationRequest) (codereview.MutationResult, error) {
	p.mutationCalls++
	return codereview.MutationResult{}, errors.New("code-change attach must not create or mutate provider changes")
}

func setupCodeChangeProfile(t *testing.T) auth.Profile {
	t.Helper()
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(codereview.OperatorProvidersFileEnv, "")
	t.Setenv("ISSUE_SPEC_TOKEN", "attach-secret")
	profile := auth.Profile{Name: "code-change-test", Kind: auth.ProfileKindHosted, Hostname: "issues.test",
		APIURL: "https://issues.test/api/v3", NativeAPIURL: "https://issues.test/api/v1",
		WebURL: "https://issues.test", ServerInstanceID: "issue-spec:code-change-test"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	return profile
}

func setupCodeChangeApp(t *testing.T, profile auth.Profile, backend *fakeCodeChangeBackend,
	provider *fakeNavigationProvider) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	app := newApp(strings.NewReader(""), out, errOut)
	app.profileName = profile.Name
	app.newNativeCodeChangeBackend = func(got auth.Profile, token string) (nativeCodeChangeBackend, error) {
		if got.Name != profile.Name || token != "attach-secret" {
			t.Fatalf("profile=%+v token=%q", got, token)
		}
		return backend, nil
	}
	app.lookupOperatorProvider = func(_ context.Context, gotProfile auth.Profile, key string) (codereview.Provider, error) {
		if gotProfile.Name != profile.Name {
			t.Fatalf("provider profile = %q, want %q", gotProfile.Name, profile.Name)
		}
		if key != backend.binding.ProviderKey {
			t.Fatalf("provider key = %q, want %q", key, backend.binding.ProviderKey)
		}
		return provider, nil
	}
	return app, out, errOut
}

func decodeCommandJSON(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode JSON %q: %v", raw, err)
	}
}
