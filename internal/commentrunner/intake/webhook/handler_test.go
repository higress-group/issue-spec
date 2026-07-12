package webhook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestCredentialsCurrentPreviousExpiryRevocationAndConstructionBounds(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	subscription := uuid.NewString()
	credentials, err := NewCredentials(subscription, Secret{Value: []byte(testCurrentSecret)}, []Secret{
		{Value: []byte(testPreviousSecret), ValidUntil: now.Add(time.Minute)},
		{Value: []byte(testExpiredSecret), ValidUntil: now.Add(-time.Second)},
		{Value: []byte(testRevokedSecret), ValidUntil: now.Add(time.Minute), Revoked: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		header string
		want   bool
	}{
		{"Bearer " + testCurrentSecret, true}, {"Bearer " + testPreviousSecret, true},
		{"Bearer " + testExpiredSecret, false}, {"Bearer " + testRevokedSecret, false},
		{"Bearer wrong", false}, {"", false}, {"Basic " + testCurrentSecret, false},
	} {
		if got := credentials.Authenticate(test.header, now); got != test.want {
			t.Fatalf("Authenticate(%q)=%v want %v", test.header, got, test.want)
		}
	}
	if len(credentials.secrets) != 4 {
		t.Fatalf("expired/revoked credentials did not participate in fixed comparison set: %d", len(credentials.secrets))
	}
	if _, err := NewCredentials(subscription, Secret{Value: []byte(testCurrentSecret)},
		[]Secret{{Value: []byte(testPreviousSecret)}}); err == nil {
		t.Fatal("previous secret without expiry accepted")
	}
	duplicate := strings.Repeat("d", MinSecretBytes)
	if _, err := NewCredentials(subscription, Secret{Value: []byte(duplicate)},
		[]Secret{{Value: []byte(duplicate), ValidUntil: now}}); err == nil {
		t.Fatal("duplicate secret accepted")
	}
	if _, err := NewCredentials(subscription, Secret{Value: []byte("short")}, nil); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestHandlerAuthenticatesValidatesPersistsAndIsIdempotentOverTLS(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	handler, queue, cleanup := testHandler(t, now, QueueConfig{MaxActiveDeliveries: 10, MaxItemBytes: 1 << 20, MaxTotalBytes: 2 << 20})
	defer cleanup()
	tlsServer := httptest.NewTLSServer(handler)
	defer tlsServer.Close()
	body, eventID := validEnvelope(t, now, "original")
	deliveryID := uuid.NewString()
	request := signedRequest(t, tlsServer.URL+Endpoint, body, deliveryID, eventID, now, testCurrentSecret)
	response, err := tlsServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || response.Header.Get("Cache-Control") != "no-store" ||
		response.Header.Get(HeaderRequestID) == "" || bytes.Contains(responseBody, []byte(testCurrentSecret)) ||
		bytes.Contains(responseBody, []byte("original")) {
		t.Fatalf("response status=%d headers=%v body=%s", response.StatusCode, response.Header, responseBody)
	}
	loaded, _ := queue.store.Load(t.Context())
	stored := loaded.Deliveries[deliveryID]
	if string(stored.RawEnvelope) != string(body) || stored.EventID != eventID || stored.SubscriptionID != queueSubscriptionID {
		t.Fatalf("stored delivery=%+v", stored)
	}
	replay := signedRequest(t, tlsServer.URL+Endpoint, body, deliveryID, eventID, now, testCurrentSecret)
	replayResponse, err := tlsServer.Client().Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	replayBody, _ := io.ReadAll(replayResponse.Body)
	replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusAccepted || !bytes.Contains(replayBody, []byte(`"duplicate":true`)) {
		t.Fatalf("replay status=%d body=%s", replayResponse.StatusCode, replayBody)
	}
	previousBody, previousEvent := validEnvelope(t, now, "previous credential")
	previousRequest := signedRequest(t, tlsServer.URL+Endpoint, previousBody, uuid.NewString(), previousEvent, now, testPreviousSecret)
	previousResponse, err := tlsServer.Client().Do(previousRequest)
	if err != nil {
		t.Fatal(err)
	}
	previousResponse.Body.Close()
	if previousResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("previous credential status=%d", previousResponse.StatusCode)
	}
}

func TestHandlerRejectsCredentialTimestampEnvelopeAndIdentityFailures(t *testing.T) {
	now := time.Unix(3000, 0).UTC()
	handler, _, cleanup := testHandler(t, now, QueueConfig{})
	defer cleanup()
	body, eventID := validEnvelope(t, now, "body")
	validDelivery := uuid.NewString()
	tests := []struct {
		name   string
		mutate func(*http.Request)
		body   []byte
		status int
	}{
		{"missing bearer", func(r *http.Request) { r.Header.Del("Authorization") }, body, http.StatusUnauthorized},
		{"wrong bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }, body, http.StatusUnauthorized},
		{"old timestamp", func(r *http.Request) { r.Header.Set(HeaderTimestamp, "1") }, body, http.StatusUnauthorized},
		{"wrong event", func(r *http.Request) { r.Header.Set(HeaderEventID, uuid.NewString()) }, body, http.StatusUnprocessableEntity},
		{"wrong subscription", func(r *http.Request) { r.Header.Set(HeaderSubscriptionID, uuid.NewString()) }, body, http.StatusUnprocessableEntity},
		{"wrong content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, body, http.StatusUnsupportedMediaType},
		{"schema header mismatch", func(r *http.Request) { r.Header.Set(HeaderSchemaVersion, "2") }, body, http.StatusUnprocessableEntity},
		{"event type header mismatch", func(r *http.Request) { r.Header.Set(HeaderEventType, "issues.created") }, body, http.StatusUnprocessableEntity},
		{"body digest header mismatch", func(r *http.Request) { r.Header.Set(HeaderEnvelopeBodySHA256, strings.Repeat("0", 64)) }, body, http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := signedRequest(t, Endpoint, test.body, validDelivery, eventID, now, testCurrentSecret)
			test.mutate(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || strings.Contains(response.Body.String(), testCurrentSecret) || strings.Contains(response.Body.String(), "body") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	for _, name := range []string{"Content-Type", "Authorization", HeaderDeliveryID, HeaderEventID,
		HeaderTimestamp, HeaderSubscriptionID, HeaderSchemaVersion, HeaderEventType, HeaderEnvelopeBodySHA256} {
		t.Run("duplicate header "+name, func(t *testing.T) {
			request := signedRequest(t, Endpoint, body, uuid.NewString(), eventID, now, testCurrentSecret)
			if request.Header.Get(name) == "" {
				request.Header.Set(name, "1")
			}
			request.Header.Add(name, request.Header.Get(name))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("duplicate %s status=%d body=%s", name, response.Code, response.Body.String())
			}
		})
	}
	mutations := []struct {
		name string
		body func([]byte) []byte
	}{
		{"unknown field", func(body []byte) []byte { return append(body[:len(body)-1], []byte(`,"parsed_command":"/goal"}`)...) }},
		{"duplicate event id", func(body []byte) []byte { return append([]byte(`{"event_id":"`+eventID+`",`), body[1:]...) }},
		{"duplicate schema", func(body []byte) []byte { return append([]byte(`{"schema_version":1,`), body[1:]...) }},
		{"duplicate body hash", func(body []byte) []byte {
			return append([]byte(`{"body_hash":"`+strings.Repeat("0", 64)+`",`), body[1:]...)
		}},
		{"schema body mismatch", func(body []byte) []byte { return mutateEnvelope(t, body, "schema_version", float64(2)) }},
		{"action mismatch", func(body []byte) []byte { return mutateEnvelope(t, body, "action", "deleted") }},
		{"raw body hash mismatch", func(body []byte) []byte { return mutateEnvelope(t, body, "raw_body", "tampered") }},
		{"missing comment snapshot", func(body []byte) []byte { return mutateEnvelope(t, body, "comment", nil) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := mutation.body(append([]byte(nil), body...))
			request := signedRequest(t, Endpoint, mutated, uuid.NewString(), eventID, now, testCurrentSecret)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	oversized := signedRequest(t, Endpoint, bytes.Repeat([]byte("x"), (1<<20)+1), uuid.NewString(), eventID, now, testCurrentSecret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d", response.Code)
	}
}

func mutateEnvelope(t *testing.T, body []byte, key string, value any) []byte {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded[key] = value
	mutated, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func TestHandlerConflictCapacityReplayAndPersistenceFailure(t *testing.T) {
	now := time.Unix(4000, 0).UTC()
	handler, _, cleanup := testHandler(t, now, QueueConfig{MaxActiveDeliveries: 1, MaxItemBytes: 1 << 20, MaxTotalBytes: 1 << 20})
	defer cleanup()
	firstBody, firstEvent := validEnvelope(t, now, "first")
	firstDelivery := uuid.NewString()
	if status := serve(handler, signedRequest(t, Endpoint, firstBody, firstDelivery, firstEvent, now, testCurrentSecret)); status != http.StatusAccepted {
		t.Fatalf("first status=%d", status)
	}
	secondBody, secondEvent := validEnvelope(t, now.Add(time.Second), "second")
	if status := serve(handler, signedRequest(t, Endpoint, secondBody, uuid.NewString(), secondEvent, now, testCurrentSecret)); status != http.StatusServiceUnavailable {
		t.Fatalf("cap status=%d", status)
	}
	if status := serve(handler, signedRequest(t, Endpoint, firstBody, firstDelivery, firstEvent, now, testCurrentSecret)); status != http.StatusAccepted {
		t.Fatalf("replay at cap status=%d", status)
	}
	if status := serve(handler, signedRequest(t, Endpoint, secondBody, firstDelivery, secondEvent, now, testCurrentSecret)); status != http.StatusConflict {
		t.Fatalf("same-id different-digest status=%d", status)
	}
	credentials, _ := NewCredentials(queueSubscriptionID, Secret{Value: []byte(testCurrentSecret)}, nil)
	failing, _ := NewHandler(HandlerConfig{Credentials: credentials, Queue: failingAcceptor{}, Clock: func() time.Time { return now }})
	if status := serve(failing, signedRequest(t, Endpoint, firstBody, uuid.NewString(), firstEvent, now, testCurrentSecret)); status != http.StatusInternalServerError {
		t.Fatalf("persistence failure status=%d", status)
	}
}

const queueSubscriptionID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

var (
	testCurrentSecret  = strings.Repeat("c", MinSecretBytes)
	testPreviousSecret = strings.Repeat("p", MinSecretBytes)
	testExpiredSecret  = strings.Repeat("e", MinSecretBytes)
	testRevokedSecret  = strings.Repeat("r", MinSecretBytes)
)

func testHandler(t *testing.T, now time.Time, queueConfig QueueConfig) (*Handler, *Queue, func()) {
	store, err := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewQueue(store, queueConfig)
	if err != nil {
		t.Fatal(err)
	}
	credentials, _ := NewCredentials(queueSubscriptionID, Secret{Value: []byte(testCurrentSecret)},
		[]Secret{{Value: []byte(testPreviousSecret), ValidUntil: now.Add(time.Minute)}})
	handler, err := NewHandler(HandlerConfig{Credentials: credentials, Queue: queue,
		Clock: func() time.Time { return now }, MaxBodyBytes: 1 << 20, MaxRawCommentBytes: 256 << 10})
	if err != nil {
		t.Fatal(err)
	}
	return handler, queue, func() { _ = store.Close() }
}

func validEnvelope(t *testing.T, at time.Time, raw string) ([]byte, string) {
	orgID, repoID, issueID, commentID, actorID, eventID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	hash := sha256.Sum256([]byte(raw))
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	snapshot := models.CommentSnapshot{Comment: models.Comment{ID: commentID, Scope: scope,
		IssueID: issueID, AuthorID: &actorID, Body: raw, RepresentationVersion: 1, CreatedAt: at, UpdatedAt: at},
		IssueNumber: 17, AuthorLogin: "runner-user"}
	envelope, _, err := outbox.BuildEnvelope(eventID, issueapi.MutationEvent{Type: "issue_comment.created", Scope: scope,
		Issue: models.Issue{ID: issueID, Scope: scope, Number: 17, CreatedAt: at.Add(-time.Hour), UpdatedAt: at}, Comment: &snapshot,
		RawBody: raw, BodyHash: hash, ActorUserID: actorID, ActorCredentialKind: serverauth.CredentialSession, RepresentationVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return body, eventID.String()
}

func signedRequest(t *testing.T, target string, body []byte, deliveryID, eventID string, at time.Time, secret string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set(HeaderDeliveryID, deliveryID)
	request.Header.Set(HeaderEventID, eventID)
	request.Header.Set(HeaderTimestamp, strconv.FormatInt(at.Unix(), 10))
	return request
}

func serve(handler http.Handler, request *http.Request) int {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code
}

type failingAcceptor struct{}

func (failingAcceptor) Accept(context.Context, state.WebhookDelivery) (Acceptance, error) {
	return Acceptance{}, errors.New("injected persistence failure containing no request material")
}
