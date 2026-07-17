package reponotifications

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/higress-group/issue-spec/internal/server/store"
)

// Eligibility is shared by the ordinary-issue preparer and the milestone
// successor. It performs the final subscription/address/account/read check.
type Eligibility interface {
	Recipient(context.Context, models.RepoScope, uuid.UUID) (store.RepositoryNotificationRecipient, error)
}

type DatabaseEligibility struct{ db store.DBTX }

func NewDatabaseEligibility(db store.DBTX) (*DatabaseEligibility, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	return &DatabaseEligibility{db: db}, nil
}

func (e *DatabaseEligibility) Recipient(ctx context.Context, scope models.RepoScope, userID uuid.UUID) (store.RepositoryNotificationRecipient, error) {
	if e == nil || e.db == nil {
		return store.RepositoryNotificationRecipient{}, ErrInvalid
	}
	return store.RepositoryNotificationRecipientForDelivery(ctx, e.db, scope, userID)
}

// Preparer owns only repo_issue_created. PROCESS-005 should add it to the
// feature preparer dispatcher rather than broadening this implementation.
type Preparer struct {
	eligibility Eligibility
	webOrigin   publicurl.Origin
}

func NewPreparer(eligibility Eligibility, webOrigin publicurl.Origin) (*Preparer, error) {
	if eligibility == nil {
		return nil, ErrInvalid
	}
	base, err := webOrigin.URL("/", nil)
	parsed, parseErr := url.Parse(base)
	if err != nil || parseErr != nil || !parsed.IsAbs() || parsed.Host == "" {
		return nil, ErrInvalid
	}
	return &Preparer{eligibility: eligibility, webOrigin: webOrigin}, nil
}

func (p *Preparer) Prepare(ctx context.Context, delivery emaildelivery.Delivery) (emaildelivery.Message, error) {
	if p == nil || delivery.Kind != emaildelivery.KindRepoIssueCreated || delivery.ID == uuid.Nil ||
		delivery.RecipientUserID == uuid.Nil || delivery.OrganizationID == nil || delivery.RepositoryID == nil ||
		delivery.IssueID == nil || *delivery.OrganizationID == uuid.Nil || *delivery.RepositoryID == uuid.Nil ||
		*delivery.IssueID == uuid.Nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	var snapshot IssueCreatedSnapshot
	if json.Unmarshal(delivery.Snapshot, &snapshot) != nil || snapshot.Validate() != nil || snapshot.IssueID != *delivery.IssueID {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	scope := models.RepoScope{OrgID: *delivery.OrganizationID, RepoID: *delivery.RepositoryID}
	recipient, err := p.eligibility.Recipient(ctx, scope, delivery.RecipientUserID)
	if errors.Is(err, store.ErrNotFound) {
		return emaildelivery.Message{}, emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	if err != nil {
		return emaildelivery.Message{}, emaildelivery.Retryable(emaildelivery.ReasonPreparationUnavailable)
	}
	path := publicurl.RepositoryResource(recipient.RepositoryOwner, recipient.RepositoryName).IssueWeb(snapshot.IssueNumber)
	link, err := p.webOrigin.URL(path, nil)
	if err != nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	subject, body, err := RenderIssueCreated(snapshot, link)
	if err != nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	return emaildelivery.Message{DeliveryID: delivery.ID, To: recipient.Address,
		Subject: subject, Body: body, OccurredAt: snapshot.OccurredAt}, nil
}
