package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestBrokerUsesSevenDayDefaultTTL(t *testing.T) {
	var requestedTTL int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var body struct {
				TTLSeconds int64 `json:"ttl_seconds"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			requestedTTL = body.TTLSeconds
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.New(), "token": delegatedTestToken("long-job"),
				"expires_at": time.Now().UTC().Add(delegation.DefaultTTL - time.Minute)})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	broker := testBroker(t, server.URL, func(context.Context) error { return nil })
	broker.TTL = 0
	broker.GitProvider = staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "runner", Password: "git-secret"},
		ExpiresAt: time.Now().UTC().Add(delegation.DefaultTTL - time.Minute), Revoke: func(context.Context) error { return nil }}}
	lease, err := broker.Acquire(t.Context(), AcquireRequest{Repo: models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()},
		JobID: "job-long-running", Binding: testBinding()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Revoke(t.Context()) }()
	if requestedTTL != int64(delegation.DefaultTTL/time.Second) {
		t.Fatalf("requested TTL=%d, want %d", requestedTTL, int64(delegation.DefaultTTL/time.Second))
	}
}

func TestBrokerAcquireAndRevokeLifecycle(t *testing.T) {
	var exchangeCalls, revokeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer parent-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/delegated-tokens/exchange"):
			exchangeCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["purpose"] != "issue-api" || body["audience"] != "instance-a" || body["subject"] != "runner-child" {
				t.Fatalf("exchange body = %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.New(), "token": delegatedTestToken("child"), "expires_at": time.Now().Add(time.Minute)})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/delegated-tokens"):
			revokeCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	providerRevoked := atomic.Int32{}
	broker := testBroker(t, server.URL, func(context.Context) error { providerRevoked.Add(1); return nil })
	repo := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	lease, err := broker.Acquire(context.Background(), AcquireRequest{Repo: repo, JobID: "job-1", Binding: testBinding()})
	if err != nil {
		t.Fatal(err)
	}
	if exchangeCalls.Load() != 1 || len(lease.FileCapabilities()) != 4 {
		t.Fatalf("exchange=%d capabilities=%+v", exchangeCalls.Load(), lease.FileCapabilities())
	}
	if err := lease.PrepareChildGit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if revokeCalls.Load() != 1 || providerRevoked.Load() != 2 {
		t.Fatalf("remote revoke=%d provider revoke=%d", revokeCalls.Load(), providerRevoked.Load())
	}
}

func TestBrokerDoesNotFollowCredentialedRedirect(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, httptest.NewRequest(http.MethodGet, target.URL, nil), target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	broker := testBroker(t, server.URL, func(context.Context) error { return nil })
	_, err := broker.Acquire(context.Background(), AcquireRequest{Repo: models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}, JobID: "job-redirect", Binding: testBinding()})
	if err == nil || redirected.Load() != 0 || strings.Contains(err.Error(), "parent-secret") {
		t.Fatalf("redirect err=%v calls=%d", err, redirected.Load())
	}
}

func TestBrokerRejectsCrossOriginNativeAPIWithoutSendingAuthorization(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("cross-origin request carried authorization: %q", r.Header.Get("Authorization"))
		}
	}))
	defer target.Close()

	broker := testBroker(t, target.URL, func(context.Context) error { return nil })
	broker.Profile.APIURL = "https://trusted.example.test/api/v3"
	broker.Profile.WebURL = "https://trusted.example.test"
	_, err := broker.Acquire(context.Background(), AcquireRequest{
		Repo: models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}, JobID: "job-cross-origin", Binding: testBinding(),
	})
	if err == nil || !strings.Contains(err.Error(), "valid self-hosted profile") {
		t.Fatalf("Acquire error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("cross-origin native endpoint received %d requests", requests.Load())
	}
}

func TestBrokerConcurrentJobsUseIsolatedFiles(t *testing.T) {
	var sequence atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		value := sequence.Add(1)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.New(), "token": delegatedTestToken(fmt.Sprintf("child-%d", value)), "expires_at": time.Now().Add(time.Minute)})
	}))
	defer server.Close()
	broker := testBroker(t, server.URL, func(context.Context) error { return nil })
	repo := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	const count = 8
	leases := make(chan *Lease, count)
	errorsCh := make(chan error, count)
	for index := range count {
		go func() {
			lease, err := broker.Acquire(context.Background(), AcquireRequest{Repo: repo, JobID: fmt.Sprintf("job-%d", index), Binding: testBinding()})
			leases <- lease
			errorsCh <- err
		}()
	}
	paths := map[string]bool{}
	var acquired []*Lease
	for range count {
		lease, err := <-leases, <-errorsCh
		if err != nil {
			t.Fatal(err)
		}
		if paths[lease.IssueToken.HostPath] {
			t.Fatalf("credential path reused: %s", lease.IssueToken.HostPath)
		}
		paths[lease.IssueToken.HostPath] = true
		acquired = append(acquired, lease)
	}
	for _, lease := range acquired {
		if err := lease.Revoke(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func delegatedTestToken(secret string) string { return "iss_dgt_aabbccdd_" + secret }

func TestBrokerEndpointPreservesNativeAPIBasePath(t *testing.T) {
	broker := &Broker{Profile: clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted,
		APIURL: "https://issues.example.test/tenant/api", NativeAPIURL: "https://issues.example.test/tenant/api/v1",
		WebURL: "https://issues.example.test/tenant", ServerInstanceID: "instance-a"}}
	endpoint, err := broker.endpoint("api/v1/orgs/o/repos/r/delegated-tokens/exchange")
	if err != nil || endpoint != "https://issues.example.test/tenant/api/v1/orgs/o/repos/r/delegated-tokens/exchange" {
		t.Fatalf("endpoint = %q, %v", endpoint, err)
	}
}

func TestBrokerRejectsNonCanonicalExchangeResponses(t *testing.T) {
	valid := func(token string, expiry time.Time) []byte {
		data, err := json.Marshal(map[string]any{"id": uuid.New(), "token": token, "expires_at": expiry})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	tests := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "missing content type", body: valid(delegatedTestToken("child"), time.Now().Add(time.Minute))},
		{name: "unknown field", contentType: "application/json", body: []byte(fmt.Sprintf(`{"id":%q,"token":%q,"expires_at":%q,"extra":true}`, uuid.New(), delegatedTestToken("child"), time.Now().Add(time.Minute).Format(time.RFC3339Nano)))},
		{name: "trailing json", contentType: "application/json", body: append(valid(delegatedTestToken("child"), time.Now().Add(time.Minute)), []byte(` {}`)...)},
		{name: "oversized", contentType: "application/json", body: append([]byte(`{"padding":"`), append(bytes.Repeat([]byte("x"), maxExchangeResponseBytes), []byte(`"}`)...)...)},
		{name: "wrong token kind", contentType: "application/json", body: valid("iss_pat_aabbccdd_secret", time.Now().Add(time.Minute))},
		{name: "expiry exceeds ttl", contentType: "application/json", body: valid(delegatedTestToken("child"), time.Now().Add(2*time.Minute))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if tt.contentType != "" {
					w.Header().Set("Content-Type", tt.contentType)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()
			broker := testBroker(t, server.URL, func(context.Context) error { return nil })
			_, err := broker.Acquire(context.Background(), AcquireRequest{Repo: models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}, JobID: "job-response", Binding: testBinding()})
			if err == nil || strings.Contains(err.Error(), delegatedTestToken("child")) {
				t.Fatalf("Acquire error = %v", err)
			}
		})
	}
}

func TestBrokerCompensatesUncertainExchangeAndBoundsCustomClient(t *testing.T) {
	var remoteRevokes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			remoteRevokes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Model a committed 201 whose response is rejected by strict decoding.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"unknown":true}`))
	}))
	defer server.Close()

	providerRevokes := atomic.Int32{}
	broker := testBroker(t, server.URL, func(context.Context) error { return nil })
	broker.GitProvider = trackingGitProvider{delegate: broker.GitProvider, revokes: &providerRevokes}
	broker.HTTPClient = &http.Client{Timeout: 0}
	if got := broker.boundedHTTPClient().Timeout; got != credentialRequestTimeout {
		t.Fatalf("zero custom client timeout = %s, want %s", got, credentialRequestTimeout)
	}
	broker.HTTPClient.Timeout = time.Hour
	if got := broker.boundedHTTPClient().Timeout; got != credentialRequestTimeout {
		t.Fatalf("overlong custom client timeout = %s, want %s", got, credentialRequestTimeout)
	}
	broker.HTTPClient.Timeout = time.Second
	if got := broker.boundedHTTPClient().Timeout; got != time.Second {
		t.Fatalf("short custom client timeout = %s", got)
	}

	_, err := broker.Acquire(context.Background(), AcquireRequest{Repo: models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()},
		JobID: "job-uncertain", Binding: testBinding()})
	if err == nil || remoteRevokes.Load() != 1 || providerRevokes.Load() != 1 {
		t.Fatalf("Acquire err=%v remote_revoke=%d provider_revoke=%d", err, remoteRevokes.Load(), providerRevokes.Load())
	}
}

func TestBrokerPreflightProvesOnlyConfiguredIssuerOperations(t *testing.T) {
	broker := testBroker(t, "https://server.example", func(context.Context) error { return nil })
	broker.Scopes = []string{"issues:read", "issues:write"}
	repo := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	report := broker.Probe(t.Context(), PreflightRequest{Request: capability.Request{Host: "server.example",
		Repository: "o/r", Operations: []capability.Operation{capability.OperationIssueRead,
			capability.OperationArtifactWrite, capability.OperationGitClone, capability.OperationGitPush}},
		Repo: repo, JobID: "job-preflight"})
	if !report.OK || !report.Credential.ExpiryKnown || report.Credential.ExpiresAt == nil || report.Backend != "operator-issuer" {
		t.Fatalf("report = %+v", report)
	}
	report = broker.Probe(t.Context(), PreflightRequest{Request: capability.Request{Host: "server.example",
		Repository: "o/r", Operations: []capability.Operation{capability.OperationPullRequestReviewWrite}},
		Repo: repo, JobID: "job-preflight"})
	if report.OK || report.Operations[0].Decision != capability.DecisionUnknown {
		t.Fatalf("unsupported report = %+v", report)
	}
}

func TestBrokerReusesStableProfilePATAcrossJobs(t *testing.T) {
	materializer := Materializer{Root: t.TempDir()}
	profileToken, err := materializer.WriteProfileToken("iss_pat_runner")
	if err != nil {
		t.Fatal(err)
	}
	profile := clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "instance-a"}
	broker := &Broker{Profile: profile, ProfileToken: &profileToken, Materializer: materializer,
		GitProvider: staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "runner", Password: "git-secret"},
			ExpiresAt: time.Now().Add(time.Minute), Revoke: func(context.Context) error { return nil }}},
		ProfileProbe: profileCapabilityProbeFunc(func(_ context.Context, request PreflightRequest) capability.Report {
			report := capability.Report{Host: request.Request.Host, Repository: request.Request.Repository,
				Backend: "rest", Credential: capability.CredentialSummary{SourceClass: "private-file"},
				Network: capability.NetworkSummary{Status: "reachable"}}
			for _, operation := range request.Request.Operations {
				report.Operations = append(report.Operations, capability.OperationResult{Operation: operation,
					Decision: capability.DecisionAllowed})
			}
			report.Finish()
			return report
		})}
	repo := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	var paths []string
	for _, jobID := range []string{"job-profile-1", "job-profile-2"} {
		lease, acquireErr := broker.Acquire(t.Context(), AcquireRequest{Repo: repo, JobID: jobID, Binding: testBinding()})
		if acquireErr != nil {
			t.Fatal(acquireErr)
		}
		paths = append(paths, lease.IssueToken.HostPath)
		if revokeErr := lease.Revoke(t.Context()); revokeErr != nil {
			t.Fatal(revokeErr)
		}
	}
	if paths[0] != profileToken.HostPath || paths[1] != profileToken.HostPath {
		t.Fatalf("profile token paths = %v, want %s", paths, profileToken.HostPath)
	}
	if _, err := os.Stat(profileToken.HostPath); err != nil {
		t.Fatalf("profile token removed after job cleanup: %v", err)
	}
	report := broker.Probe(t.Context(), PreflightRequest{Request: capability.Request{Host: "issues.example.test",
		Repository: "o/r", Operations: []capability.Operation{capability.OperationIssueRead, capability.OperationGitPush}},
		Repo: repo, JobID: "job-profile-probe"})
	if !report.OK || report.Credential.ExpiryKnown || report.Credential.SourceClass != "private-file" ||
		report.Backend != "profile-credential" {
		t.Fatalf("profile report = %+v", report)
	}
}

func TestBrokerProfilePreflightUsesLiveIssueProbeForEveryJob(t *testing.T) {
	materializer := Materializer{Root: t.TempDir()}
	profileToken, err := materializer.WriteProfileToken("iss_pat_runner")
	if err != nil {
		t.Fatal(err)
	}
	profile := clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "instance-a"}
	var calls atomic.Int32
	broker := &Broker{Profile: profile, ProfileToken: &profileToken, Materializer: materializer,
		GitProvider: staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "runner", Password: "git-secret"},
			ExpiresAt: time.Now().Add(time.Minute), Revoke: func(context.Context) error { return nil }}},
		ProfileProbe: profileCapabilityProbeFunc(func(_ context.Context, request PreflightRequest) capability.Report {
			call := calls.Add(1)
			report := capability.Report{Host: request.Request.Host, Repository: request.Request.Repository,
				Credential: capability.CredentialSummary{SourceClass: "private-file"},
				Network:    capability.NetworkSummary{Status: "reachable"}}
			for _, operation := range request.Request.Operations {
				result := capability.OperationResult{Operation: operation, Decision: capability.DecisionAllowed}
				if call == 2 && operation == capability.OperationArtifactWrite {
					result.Decision, result.Code = capability.DecisionDenied, capability.FailureInsufficientPermission
				}
				report.Operations = append(report.Operations, result)
			}
			report.Finish()
			return report
		})}
	repo := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	request := PreflightRequest{Request: capability.Request{Host: "issues.example.test", Repository: "o/r",
		Operations: []capability.Operation{capability.OperationIssueRead, capability.OperationArtifactWrite,
			capability.OperationGitPush}}, Repo: repo, JobID: "job-profile-live"}
	first := broker.Probe(t.Context(), request)
	request.JobID = "job-profile-revoked"
	second := broker.Probe(t.Context(), request)
	if calls.Load() != 2 || !first.OK || second.OK || second.Network.Status != "reachable" {
		t.Fatalf("calls=%d first=%+v second=%+v", calls.Load(), first, second)
	}
	results := make(map[capability.Operation]capability.OperationResult, len(second.Operations))
	for _, result := range second.Operations {
		results[result.Operation] = result
	}
	if results[capability.OperationArtifactWrite].Decision != capability.DecisionDenied ||
		results[capability.OperationGitPush].Decision != capability.DecisionAllowed {
		t.Fatalf("live issue and Git results were not merged independently: %+v", second)
	}
}

type profileCapabilityProbeFunc func(context.Context, PreflightRequest) capability.Report

func (f profileCapabilityProbeFunc) ProbeProfileCredential(ctx context.Context, request PreflightRequest) capability.Report {
	return f(ctx, request)
}

type trackingGitProvider struct {
	delegate GitProvider
	revokes  *atomic.Int32
}

func (p trackingGitProvider) Acquire(ctx context.Context, request GitRequest) (GitProviderLease, error) {
	return p.delegate.Acquire(ctx, request)
}

func (p trackingGitProvider) RevokeJob(ctx context.Context, jobID string) error {
	p.revokes.Add(1)
	return p.delegate.RevokeJob(ctx, jobID)
}

func testBroker(t *testing.T, origin string, revoke func(context.Context) error) *Broker {
	t.Helper()
	profile := clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted, APIURL: origin + "/api/v3",
		NativeAPIURL: origin + "/api/v1", WebURL: origin, ServerInstanceID: "instance-a"}
	return &Broker{Profile: profile, Audience: "instance-a", Subject: "runner-child", ParentToken: "parent-secret",
		Materializer: Materializer{Root: t.TempDir()}, TTL: time.Minute,
		GitProvider: staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "runner", Password: "git-secret"},
			ExpiresAt: time.Now().Add(time.Minute), Revoke: revoke}}}
}
