package reponotifications

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type eligibilityFunc func(context.Context, models.RepoScope, uuid.UUID) (store.RepositoryNotificationRecipient, error)

func (f eligibilityFunc) Recipient(ctx context.Context, scope models.RepoScope, userID uuid.UUID) (store.RepositoryNotificationRecipient, error) {
	return f(ctx, scope, userID)
}

func TestPreparerRevalidatesEligibilityAndRendersCanonicalLink(t *testing.T) {
	orgID, repoID, issueID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	origin, err := publicurl.ParseOrigin("web", "https://issues.example.test")
	if err != nil {
		t.Fatal(err)
	}
	eligibility := eligibilityFunc(func(_ context.Context, scope models.RepoScope, gotUser uuid.UUID) (store.RepositoryNotificationRecipient, error) {
		if scope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || gotUser != userID {
			t.Fatalf("eligibility input = %+v %s", scope, gotUser)
		}
		return store.RepositoryNotificationRecipient{RepositoryNotificationCandidate: store.RepositoryNotificationCandidate{
			UserID: userID, Login: "reader", DisplayName: "Reader",
		}, Address: "reader@example.test", RepositoryOwner: "acme", RepositoryName: "widgets"}, nil
	})
	preparer, err := NewPreparer(eligibility, origin, emaildelivery.AddressPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	delivery := issueDelivery(t, orgID, repoID, issueID, userID)
	message, err := preparer.Prepare(t.Context(), delivery)
	if err != nil || message.To != "reader@example.test" || message.DeliveryID != delivery.ID ||
		message.OccurredAt.IsZero() || !containsAll(message.Body, "acme/widgets", "/acme/widgets/issues/7") {
		t.Fatalf("prepared message = %+v err=%v", message, err)
	}
}

func TestPreparerSuppressesUnsubscribeAndDoesNotExposePolicyDetails(t *testing.T) {
	orgID, repoID, issueID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	origin, _ := publicurl.ParseOrigin("web", "https://issues.example.test")
	preparer, _ := NewPreparer(eligibilityFunc(func(context.Context, models.RepoScope, uuid.UUID) (store.RepositoryNotificationRecipient, error) {
		return store.RepositoryNotificationRecipient{}, store.ErrNotFound
	}), origin, emaildelivery.AddressPolicy{})
	_, err := preparer.Prepare(t.Context(), issueDelivery(t, orgID, repoID, issueID, userID))
	var outcome *emaildelivery.OutcomeError
	if !errors.As(err, &outcome) || !outcome.Suppressed || outcome.Reason != emaildelivery.ReasonRecipientUnavailable {
		t.Fatalf("suppression error = %#v / %v", outcome, err)
	}
	for _, forbidden := range []string{"subscription", "address", "repository"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("suppression leaked %q: %v", forbidden, err)
		}
	}
}

func TestPreparerSuppressesAddressOutsideCurrentDomainPolicy(t *testing.T) {
	orgID, repoID, issueID, userID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	origin, _ := publicurl.ParseOrigin("web", "https://issues.example.test")
	policy, err := emaildelivery.NewAddressPolicy([]string{"corp.example"})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := NewPreparer(eligibilityFunc(func(context.Context, models.RepoScope,
		uuid.UUID) (store.RepositoryNotificationRecipient, error) {
		return store.RepositoryNotificationRecipient{Address: "reader@personal.example",
			RepositoryOwner: "acme", RepositoryName: "widgets"}, nil
	}), origin, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = preparer.Prepare(t.Context(), issueDelivery(t, orgID, repoID, issueID, userID))
	var outcome *emaildelivery.OutcomeError
	if !errors.As(err, &outcome) || !outcome.Suppressed || outcome.Reason != emaildelivery.ReasonRecipientUnavailable {
		t.Fatalf("Prepare() error = %#v / %v", outcome, err)
	}
}

func issueDelivery(t *testing.T, orgID, repoID, issueID, userID uuid.UUID) emaildelivery.Delivery {
	t.Helper()
	snapshot := IssueCreatedSnapshot{Version: 1, ActorLogin: "author", ActorDisplayName: "Author",
		RepositoryOwner: "acme", RepositoryName: "widgets", IssueID: issueID, IssueNumber: 7,
		IssueTitle: "Created issue", IssueBody: "body", OccurredAt: time.Now().UTC()}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return emaildelivery.Delivery{ID: uuid.New(), Kind: emaildelivery.KindRepoIssueCreated,
		RecipientUserID: userID, OrganizationID: &orgID, RepositoryID: &repoID,
		IssueID: &issueID, Snapshot: payload}
}

func containsAll(value string, expected ...string) bool {
	for _, item := range expected {
		if !strings.Contains(value, item) {
			return false
		}
	}
	return true
}
