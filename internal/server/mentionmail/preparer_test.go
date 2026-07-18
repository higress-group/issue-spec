package mentionmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
)

func TestMentionPreparerRechecksRecipientAndReadAuthorityBeforeSend(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	delivery := mentionDelivery(t, now)
	preparer := &Preparer{loader: staticRecipient{address: "recipient@example.test"},
		authorizer: staticAuthorizer{allowed: true}, webOrigin: mustURL(t, "https://issues.example.test/")}
	message, err := preparer.Prepare(t.Context(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	if message.To != "recipient@example.test" || message.DeliveryID != delivery.ID ||
		!strings.Contains(message.Body, "https://issues.example.test/acme/widgets/issues/17#issuecomment-42") ||
		!strings.Contains(message.Body, "@author") {
		t.Fatalf("message = %+v", message)
	}
	if strings.Contains(string(delivery.Snapshot), message.To) || strings.Contains(delivery.String(), message.To) {
		t.Fatal("delivery persistence or diagnostics leaked the live recipient address")
	}
}

func TestMentionPreparerRunsThroughFoundationWorkerWithFakeSender(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
	delivery := mentionDelivery(t, now)
	delivery.State, delivery.Attempts, delivery.RepresentationVersion = emaildelivery.StateDelivering, 1, 2
	queue := &mentionWorkerQueue{claim: &emaildelivery.Claim{Delivery: delivery, LeaseVersion: 2}}
	sender := &mentionSender{}
	preparer := &Preparer{loader: staticRecipient{address: "recipient@example.test"},
		authorizer: staticAuthorizer{allowed: true}, webOrigin: mustURL(t, "https://issues.example.test/")}
	worker, err := emaildelivery.NewWorker(queue, preparer, sender, emaildelivery.WorkerConfig{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if queue.state != emaildelivery.StateSucceeded || len(sender.messages) != 1 ||
		sender.messages[0].To != "recipient@example.test" {
		t.Fatalf("worker state=%s messages=%+v", queue.state, sender.messages)
	}
}

func TestMentionPreparerSuppressesRecipientOrAuthorizationChanges(t *testing.T) {
	delivery := mentionDelivery(t, time.Now().UTC())
	for _, test := range []struct {
		name       string
		loader     recipientLoader
		authorizer RepositoryAuthorizer
	}{
		{name: "verified address removed", loader: staticRecipient{err: emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)}, authorizer: staticAuthorizer{allowed: true}},
		{name: "repository read removed", loader: staticRecipient{address: "recipient@example.test"}, authorizer: staticAuthorizer{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			preparer := &Preparer{loader: test.loader, authorizer: test.authorizer,
				webOrigin: mustURL(t, "https://issues.example.test/")}
			_, err := preparer.Prepare(t.Context(), delivery)
			var outcome *emaildelivery.OutcomeError
			if err == nil || !strings.Contains(err.Error(), "email delivery failed") || !errors.As(err, &outcome) || !outcome.Suppressed {
				t.Fatalf("Prepare() error = %v", err)
			}
		})
	}
}

func TestMentionPreparerSuppressesAddressOutsideCurrentDomainPolicy(t *testing.T) {
	policy, err := emaildelivery.NewAddressPolicy([]string{"corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	preparer := &Preparer{loader: staticRecipient{address: "recipient@personal.example"},
		authorizer: staticAuthorizer{allowed: true}, webOrigin: mustURL(t, "https://issues.example.test/"),
		policy: policy}
	_, err = preparer.Prepare(t.Context(), mentionDelivery(t, time.Now().UTC()))
	var outcome *emaildelivery.OutcomeError
	if !errors.As(err, &outcome) || !outcome.Suppressed || outcome.Reason != emaildelivery.ReasonRecipientUnavailable {
		t.Fatalf("Prepare() error = %#v / %v", outcome, err)
	}
}

func mentionDelivery(t *testing.T, now time.Time) emaildelivery.Delivery {
	t.Helper()
	orgID, repoID, commentID := uuid.New(), uuid.New(), uuid.New()
	snapshot, err := json.Marshal(Snapshot{Version: SnapshotVersion, ActorLogin: "author",
		ActorDisplayName: "Author", Organization: "acme", Repository: "widgets",
		IssueNumber: 17, IssueTitle: "Private issue", CommentID: commentID,
		CommentNumericID: 42, Excerpt: "Hello @recipient", OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return emaildelivery.Delivery{ID: uuid.New(), Kind: emaildelivery.KindMention,
		RecipientUserID: uuid.New(), OrganizationID: &orgID, RepositoryID: &repoID,
		CommentID: &commentID, Snapshot: snapshot, CreatedAt: now}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

type staticRecipient struct {
	address string
	err     error
}

func (s staticRecipient) loadRecipient(context.Context, uuid.UUID) (string, error) {
	return s.address, s.err
}

type staticAuthorizer struct {
	allowed bool
	err     error
}

func (s staticAuthorizer) EvaluateRepository(context.Context, authz.Subject, authz.RepositoryRequest) (authz.Decision, error) {
	return authz.Decision{Exists: true, Visible: true, Allowed: s.allowed}, s.err
}

type mentionWorkerQueue struct {
	claim *emaildelivery.Claim
	state emaildelivery.State
}

func (q *mentionWorkerQueue) ClaimOne(context.Context, time.Time, time.Duration) (*emaildelivery.Claim, error) {
	if q.claim == nil {
		return nil, emaildelivery.ErrNoWork
	}
	claim := q.claim
	q.claim = nil
	return claim, nil
}

func (q *mentionWorkerQueue) Succeed(context.Context, *emaildelivery.Claim, time.Time) error {
	q.state = emaildelivery.StateSucceeded
	return nil
}

func (q *mentionWorkerQueue) Retry(context.Context, *emaildelivery.Claim, time.Time, time.Time, emaildelivery.ReasonCode) error {
	q.state = emaildelivery.StatePending
	return nil
}

func (q *mentionWorkerQueue) Fail(context.Context, *emaildelivery.Claim, time.Time, emaildelivery.ReasonCode) error {
	q.state = emaildelivery.StateFailed
	return nil
}

func (q *mentionWorkerQueue) Suppress(context.Context, *emaildelivery.Claim, time.Time, emaildelivery.ReasonCode) error {
	q.state = emaildelivery.StateSuppressed
	return nil
}

type mentionSender struct{ messages []emaildelivery.Message }

func (s *mentionSender) Send(_ context.Context, message emaildelivery.Message) error {
	s.messages = append(s.messages, message)
	return nil
}

var _ emaildelivery.Queue = (*mentionWorkerQueue)(nil)
var _ emaildelivery.Sender = (*mentionSender)(nil)
