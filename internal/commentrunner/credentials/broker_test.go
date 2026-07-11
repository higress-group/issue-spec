package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

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

func testBroker(t *testing.T, origin string, revoke func(context.Context) error) *Broker {
	t.Helper()
	profile := clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted, APIURL: origin + "/api/v3",
		NativeAPIURL: origin + "/api/v1", WebURL: origin, ServerInstanceID: "instance-a"}
	return &Broker{Profile: profile, Audience: "instance-a", Subject: "runner-child", ParentToken: "parent-secret",
		Materializer: Materializer{Root: t.TempDir()}, TTL: time.Minute,
		GitProvider: staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "runner", Password: "git-secret"},
			ExpiresAt: time.Now().Add(time.Minute), Revoke: revoke}}}
}
