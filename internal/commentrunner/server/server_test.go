package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestServiceGracefulShutdownWaitsForInflightPersistence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	acceptor := &blockingAcceptor{entered: make(chan struct{}), release: make(chan struct{})}
	credentials, _ := webhook.NewCredentials(uuid.NewString(), webhook.Secret{Value: []byte(serverTestSecret)}, nil)
	handler, _ := webhook.NewHandler(webhook.HandlerConfig{Credentials: credentials, Queue: acceptor,
		Clock: func() time.Time { return now }})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Listener: listener, ShutdownTimeout: 3 * time.Second}, handler)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runResult := make(chan error, 1)
	go func() { runResult <- service.Run(ctx) }()
	waitReady(t, "http://"+listener.Addr().String()+"/readyz")
	body, eventID := serverEnvelope(t, now)
	request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+webhook.Endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+serverTestSecret)
	request.Header.Set(webhook.HeaderDeliveryID, uuid.NewString())
	request.Header.Set(webhook.HeaderEventID, eventID)
	request.Header.Set(webhook.HeaderTimestamp, stringInt(now.Unix()))
	responseResult := make(chan *http.Response, 1)
	requestErr := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			requestErr <- err
			return
		}
		responseResult <- response
	}()
	select {
	case <-acceptor.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not enter durable acceptor")
	}
	cancel()
	select {
	case err := <-runResult:
		t.Fatalf("server stopped before in-flight persistence completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(acceptor.release)
	select {
	case err := <-requestErr:
		t.Fatal(err)
	case response := <-responseResult:
		response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("response status=%d", response.StatusCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not finish")
	}
	if err := <-runResult; err != nil {
		t.Fatal(err)
	}
	if handler.Ready() {
		t.Fatal("handler remained ready after shutdown")
	}
}

func TestServerBindPolicyAppliesToConfiguredAndInjectedListeners(t *testing.T) {
	handler := minimalHandler(t)
	for _, test := range []struct {
		name   string
		config Config
	}{
		{"wildcard", Config{ListenAddress: "0.0.0.0:8080"}},
		{"plaintext non-loopback", Config{ListenAddress: "192.0.2.10:8080"}},
		{"production without TLS", Config{ListenAddress: "192.0.2.10:8443", Production: true}},
		{"production loopback", Config{ListenAddress: "127.0.0.1:8443", Production: true, TLSCertFile: "cert", TLSKeyFile: "key"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config, handler); err == nil {
				t.Fatal("unsafe bind accepted")
			}
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := New(Config{Listener: listener, Production: true, TLSCertFile: "cert", TLSKeyFile: "key"}, handler); err == nil {
		t.Fatal("injected listener bypassed production bind policy")
	}
	if _, err := New(Config{ListenAddress: "127.0.0.1:0"}, handler); err != nil {
		t.Fatalf("loopback development bind denied: %v", err)
	}
}

func TestServerBoundsRequestHeaders(t *testing.T) {
	handler := minimalHandler(t)
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	service, err := New(Config{Listener: listener, MaxHeaderBytes: 1024}, handler)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	waitReady(t, "http://"+listener.Addr().String()+"/readyz")
	request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+webhook.Endpoint, strings.NewReader(`{}`))
	request.Header.Set("X-Oversized", strings.Repeat("x", 16<<10))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("large header status=%d", response.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceServesTLS12FromPrivateKeyHandle(t *testing.T) {
	seed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	seed.Close()
	certificate := seed.TLS.Certificates[0]
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	service, err := New(Config{Listener: listener, TLSCertFile: certPath, TLSKeyFile: keyPath}, minimalHandler(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}}} // test-only certificate trust
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := client.Get("https://" + listener.Addr().String() + "/readyz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("TLS ready status=%d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func minimalHandler(t *testing.T) *webhook.Handler {
	credentials, _ := webhook.NewCredentials(uuid.NewString(), webhook.Secret{Value: []byte(serverTestSecret)}, nil)
	handler, err := webhook.NewHandler(webhook.HandlerConfig{Credentials: credentials, Queue: &blockingAcceptor{}})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

var serverTestSecret = strings.Repeat("s", webhook.MinSecretBytes)

type blockingAcceptor struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *blockingAcceptor) Accept(_ context.Context, delivery state.WebhookDelivery) (webhook.Acceptance, error) {
	if a.entered != nil {
		a.once.Do(func() { close(a.entered) })
	}
	if a.release != nil {
		<-a.release
	}
	return webhook.Acceptance{Delivery: delivery}, nil
}

func waitReady(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not become ready")
}

func serverEnvelope(t *testing.T, at time.Time) ([]byte, string) {
	orgID, repoID, issueID, commentID, actorID, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	raw := "runner command"
	hash := sha256.Sum256([]byte(raw))
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	snapshot := models.CommentSnapshot{Comment: models.Comment{ID: commentID, Scope: scope, IssueID: issueID,
		AuthorID: &actorID, Body: raw, RepresentationVersion: 1, CreatedAt: at, UpdatedAt: at},
		IssueNumber: 1, AuthorLogin: "runner"}
	envelope, _, err := outbox.BuildEnvelope(eventID, issueapi.MutationEvent{Type: "issue_comment.created", Scope: scope,
		Issue: models.Issue{ID: issueID, Scope: scope, Number: 1}, Comment: &snapshot, RawBody: raw,
		BodyHash: hash, ActorUserID: actorID, RepresentationVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(envelope)
	return body, eventID.String()
}

func stringInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
