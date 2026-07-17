package profilemail

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
)

func TestVerificationPreparerWithFoundationWorkerSucceedsWithoutQueueSecrets(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	secrets := testSecrets(t)
	token, _, err := secrets.RandomToken("email_verify")
	if err != nil {
		t.Fatal(err)
	}
	requestID := uuid.New()
	ciphertext, err := secrets.Encrypt(tokenCipherPurpose(requestID), []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	address := "person@example.test"
	claim := verificationClaim(requestID, now)
	queue := &workerQueue{claim: claim}
	preparer := testPreparer(secrets, staticLoader{request: preparedVerification{
		RequestID: requestID, UserID: claim.RecipientUserID, Address: address,
		Ciphertext: ciphertext, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}}, now)
	sender := &recordingSender{}
	worker, err := emaildelivery.NewWorker(queue, preparer, sender, emaildelivery.WorkerConfig{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if queue.state != emaildelivery.StateSucceeded || len(sender.messages) != 1 {
		t.Fatalf("delivery = %s messages=%d", queue.state, len(sender.messages))
	}
	message := sender.messages[0]
	if message.To != address || !strings.Contains(message.Body, url.QueryEscape(token)) {
		t.Fatalf("prepared message did not contain live recipient/token: to=%q body=%q", message.To, message.Body)
	}
	if !strings.Contains(message.Body, "#token=") || strings.Contains(message.Body, "?token=") {
		t.Fatalf("confirmation token was not isolated in the browser fragment: %q", message.Body)
	}
	stored := string(claim.Snapshot)
	if strings.Contains(stored, address) || strings.Contains(stored, token) || bytes.Contains(storedBytes(claim), ciphertext) {
		t.Fatal("delivery queue representation contains verification secret material")
	}
	if strings.Contains(claim.String(), address) || strings.Contains(claim.String(), token) {
		t.Fatal("delivery diagnostic string contains verification secret material")
	}
}

func TestVerificationWorkerRetriesWithOnlyStableReason(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	secrets := testSecrets(t)
	token, _, _ := secrets.RandomToken("email_verify")
	requestID := uuid.New()
	ciphertext, _ := secrets.Encrypt(tokenCipherPurpose(requestID), []byte(token))
	claim := verificationClaim(requestID, now)
	queue := &workerQueue{claim: claim}
	preparer := testPreparer(secrets, staticLoader{request: preparedVerification{
		RequestID: requestID, UserID: claim.RecipientUserID, Address: "retry@example.test",
		Ciphertext: ciphertext, ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}}, now)
	sender := &recordingSender{errors: []error{emaildelivery.Retryable(emaildelivery.ReasonSMTPUnavailable)}}
	worker, err := emaildelivery.NewWorker(queue, preparer, sender, emaildelivery.WorkerConfig{
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if queue.state != emaildelivery.StatePending || queue.reason != emaildelivery.ReasonSMTPUnavailable ||
		!queue.next.Equal(now.Add(emaildelivery.InitialBackoff)) {
		t.Fatalf("retry = %s/%s at %s", queue.state, queue.reason, queue.next)
	}
	if strings.Contains(string(queue.reason), token) || strings.Contains(string(queue.reason), "retry@example.test") {
		t.Fatal("retry state leaked private message data")
	}
}

func TestVerificationPreparerSuppressesExpiredOrSupersededRequests(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	secrets := testSecrets(t)
	requestID := uuid.New()
	claim := verificationClaim(requestID, now)

	for _, test := range []struct {
		name   string
		loader verificationLoader
	}{
		{name: "superseded", loader: staticLoader{err: emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)}},
		{name: "expired", loader: staticLoader{request: preparedVerification{
			RequestID: requestID, UserID: claim.RecipientUserID, Address: "expired@example.test",
			Ciphertext: []byte("unread"), ExpiresAt: now, CreatedAt: now.Add(-time.Hour),
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			queue := &workerQueue{claim: verificationClaim(requestID, now)}
			preparer := testPreparer(secrets, test.loader, now)
			sender := &recordingSender{}
			worker, err := emaildelivery.NewWorker(queue, preparer, sender, emaildelivery.WorkerConfig{Clock: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if queue.state != emaildelivery.StateSuppressed || queue.reason != emaildelivery.ReasonRecipientUnavailable || len(sender.messages) != 0 {
				t.Fatalf("suppression = %s/%s messages=%d", queue.state, queue.reason, len(sender.messages))
			}
		})
	}
}

type staticLoader struct {
	request preparedVerification
	err     error
}

func (l staticLoader) loadVerification(context.Context, uuid.UUID, uuid.UUID) (preparedVerification, error) {
	return l.request, l.err
}

func testPreparer(secrets *serverauth.Secrets, loader verificationLoader, now time.Time) *VerificationPreparer {
	base, _ := url.Parse("https://issues.example.test/settings/email/confirm")
	return &VerificationPreparer{loader: loader, secrets: secrets, baseURL: base,
		subject: "Confirm your notification email", now: func() time.Time { return now }}
}

func testSecrets(t *testing.T) *serverauth.Secrets {
	t.Helper()
	secrets, err := serverauth.NewSecrets(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return secrets
}

func verificationClaim(requestID uuid.UUID, now time.Time) *emaildelivery.Claim {
	userID, deliveryID := uuid.New(), uuid.New()
	return &emaildelivery.Claim{Delivery: emaildelivery.Delivery{
		ID: deliveryID, Kind: emaildelivery.KindVerification, IdempotencyKey: requestID.String(),
		RecipientUserID: userID, VerificationRequestID: &requestID,
		Snapshot: []byte(`{"template":"notification_email_verification","version":1}`),
		State:    emaildelivery.StateDelivering, Attempts: 1, RepresentationVersion: 2,
		CreatedAt: now,
	}, LeaseVersion: 2}
}

func storedBytes(claim *emaildelivery.Claim) []byte {
	return []byte(claim.String() + string(claim.Snapshot))
}

type recordingSender struct {
	mu       sync.Mutex
	messages []emaildelivery.Message
	errors   []error
}

func (s *recordingSender) Send(_ context.Context, message emaildelivery.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	if len(s.errors) == 0 {
		return nil
	}
	err := s.errors[0]
	s.errors = s.errors[1:]
	return err
}

type workerQueue struct {
	mu     sync.Mutex
	claim  *emaildelivery.Claim
	state  emaildelivery.State
	reason emaildelivery.ReasonCode
	next   time.Time
}

func (q *workerQueue) ClaimOne(context.Context, time.Time, time.Duration) (*emaildelivery.Claim, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claim == nil {
		return nil, emaildelivery.ErrNoWork
	}
	claim := q.claim
	q.claim = nil
	return claim, nil
}

func (q *workerQueue) Succeed(context.Context, *emaildelivery.Claim, time.Time) error {
	q.state = emaildelivery.StateSucceeded
	return nil
}

func (q *workerQueue) Retry(_ context.Context, _ *emaildelivery.Claim, next, _ time.Time, reason emaildelivery.ReasonCode) error {
	q.state, q.reason, q.next = emaildelivery.StatePending, reason, next
	return nil
}

func (q *workerQueue) Fail(_ context.Context, _ *emaildelivery.Claim, _ time.Time, reason emaildelivery.ReasonCode) error {
	q.state, q.reason = emaildelivery.StateFailed, reason
	return nil
}

func (q *workerQueue) Suppress(_ context.Context, _ *emaildelivery.Claim, _ time.Time, reason emaildelivery.ReasonCode) error {
	q.state, q.reason = emaildelivery.StateSuppressed, reason
	return nil
}

var _ emaildelivery.Sender = (*recordingSender)(nil)
var _ emaildelivery.Queue = (*workerQueue)(nil)
