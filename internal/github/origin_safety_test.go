package github

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientRejectsCrossOriginRedirectBeforeSendingToken(t *testing.T) {
	const secret = "redirect-secret-token"
	var attackerRequests atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		attackerRequests.Add(1)
	}))
	defer attacker.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", attacker.URL+"/steal")
		w.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()

	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: origin.URL, Token: secret, HTTPClient: origin.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.GetUser(context.Background())
	if err == nil {
		t.Fatal("cross-origin redirect succeeded")
	}
	var unsafe *UnsafeOriginError
	if !errors.As(err, &unsafe) || unsafe.Operation != "cross-origin redirect" {
		t.Fatalf("error = %T %v", err, err)
	}
	if attackerRequests.Load() != 0 {
		t.Fatalf("attacker received %d requests", attackerRequests.Load())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked in error: %v", err)
	}
}

func TestAbsoluteCursorOriginSafetyRunsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	doer := roundTripDoer(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("should not run")
	})
	client := NewClientWithHTTPDoer("issues.test", "https://issues.test/api", "cursor-secret", doer)
	_, err := client.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{
		Page: RunnerPageOptions{CursorURL: "https://evil.test/repos/o/r/issues/comments?page=2"},
	})
	if err == nil {
		t.Fatal("cross-origin cursor succeeded")
	}
	var unsafe *UnsafeOriginError
	if !errors.As(err, &unsafe) || unsafe.Operation != "absolute cursor" {
		t.Fatalf("error = %T %v", err, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("transport received %d requests", requests.Load())
	}
}

func TestMaliciousCursorFormsAreRejectedAndSafeFormsSucceed(t *testing.T) {
	var requests atomic.Int32
	var requested []string
	doer := roundTripDoer(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		requested = append(requested, r.URL.String())
		if r.Header.Get("Authorization") != "Bearer cursor-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("[]"))}, nil
	})
	client := NewClientWithHTTPDoer("issues.test", "https://issues.test/api", "cursor-secret", doer)
	malicious := []string{
		"//evil.test/repos/o/r/issues/comments?page=2",
		"https://user:password@issues.test/api/repos/o/r/issues/comments?page=2",
		"http://issues.test/api/repos/o/r/issues/comments?page=2",
		"https://issues.test:8443/api/repos/o/r/issues/comments?page=2",
	}
	for _, cursor := range malicious {
		if _, err := client.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{Page: RunnerPageOptions{CursorURL: cursor}}); err == nil {
			t.Fatalf("malicious cursor %q accepted", cursor)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("malicious cursors reached transport %d times", requests.Load())
	}
	safe := []string{
		"/repos/o/r/issues/comments?page=2",
		"https://issues.test/api/repos/o/r/issues/comments?page=3",
	}
	for _, cursor := range safe {
		if _, err := client.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{Page: RunnerPageOptions{CursorURL: cursor}}); err != nil {
			t.Fatalf("safe cursor %q rejected: %v", cursor, err)
		}
	}
	if requests.Load() != 2 || len(requested) != 2 || requested[0] != "https://issues.test/api/repos/o/r/issues/comments?page=2" || requested[1] != safe[1] {
		t.Fatalf("requested URLs = %#v count=%d", requested, requests.Load())
	}
}

func TestClientAllowsSameOriginRedirectAndPreservesAuthorization(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer same-origin-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/user" {
			w.Header().Set("Location", "/actual-user")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL, Token: "same-origin-secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := client.GetUser(context.Background())
	if err != nil || user.Login != "alice" || requests.Load() != 2 {
		t.Fatalf("user=%+v requests=%d err=%v", user, requests.Load(), err)
	}
}

func TestSameOriginDifferentAPIBasePathIsRejectedBeforeCredentialForward(t *testing.T) {
	var tenantBRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tenant-a/user":
			w.Header().Set("Location", "/tenant-b/user")
			w.WriteHeader(http.StatusFound)
		case "/tenant-b/user":
			tenantBRequests.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL + "/tenant-a", Token: "tenant-a-secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.GetUser(context.Background()); err == nil {
		t.Fatal("same-origin cross-base redirect succeeded")
	}
	if tenantBRequests.Load() != 0 {
		t.Fatalf("tenant-b received %d requests", tenantBRequests.Load())
	}
	if _, err := client.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{Page: RunnerPageOptions{CursorURL: server.URL + "/tenant-b/comments?page=2"}}); err == nil {
		t.Fatal("same-origin cross-base cursor succeeded")
	}
	if tenantBRequests.Load() != 0 {
		t.Fatalf("tenant-b received cursor request; count=%d", tenantBRequests.Load())
	}
}

func TestOriginComparisonHandlesIPv6DefaultPortAndRejectsPortChange(t *testing.T) {
	base, origin, err := canonicalAPIBase("https://[::1]:443/api")
	if err != nil {
		t.Fatal(err)
	}
	if origin != "https://[::1]" {
		t.Fatalf("origin = %q", origin)
	}
	if got, err := resolveEndpoint(base, origin, "https://[::1]/api/items?page=2"); err != nil || got == "" {
		t.Fatalf("same-origin IPv6 cursor = %q err=%v", got, err)
	}
	for _, cursor := range []string{"http://[::1]/api/items?page=2", "https://[::1]:8443/api/items?page=2"} {
		if _, err := resolveEndpoint(base, origin, cursor); err == nil {
			t.Fatalf("unsafe cursor %q accepted", cursor)
		}
	}
}

func TestClientCustomCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"login":"alice"}`))
	}))
	defer server.Close()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("TLS server certificate unavailable")
	}
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	if err := os.WriteFile(caPath, block, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClientWithOptions(ClientOptions{Host: "issues.test", BaseURL: server.URL, Token: "token", CAFile: caPath})
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := client.GetUser(context.Background())
	if err != nil || user.Login != "alice" {
		t.Fatalf("user = %+v err=%v", user, err)
	}
}
