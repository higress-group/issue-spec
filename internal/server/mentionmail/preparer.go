package mentionmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

type recipientLoader interface {
	loadRecipient(context.Context, uuid.UUID) (string, error)
}

type databaseRecipientLoader struct{ db serverstore.DBTX }

func (l databaseRecipientLoader) loadRecipient(ctx context.Context, userID uuid.UUID) (string, error) {
	var address string
	err := l.db.QueryRow(ctx, `SELECT u.notification_email FROM users u
		WHERE u.id = $1 AND u.status = 'active'
		AND u.notification_email IS NOT NULL AND u.notification_email_verified_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM service_accounts account WHERE account.user_id = u.id)`, userID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	if err != nil {
		return "", emaildelivery.Retryable(emaildelivery.ReasonPreparationUnavailable)
	}
	if strings.TrimSpace(address) == "" || strings.ContainsAny(address, "\r\n") {
		return "", emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	return address, nil
}

type RepositoryAuthorizer interface {
	EvaluateRepository(context.Context, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
}

// Preparer performs the second recipient and read-authority check immediately
// before the shared worker calls its Sender. It never renders private snapshot
// content for an ineligible recipient.
type Preparer struct {
	loader     recipientLoader
	authorizer RepositoryAuthorizer
	webOrigin  *url.URL
	policy     emaildelivery.AddressPolicy
}

func NewPreparer(db serverstore.DBTX, authorizer RepositoryAuthorizer, webOrigin string,
	policy emaildelivery.AddressPolicy) (*Preparer, error) {
	if db == nil || authorizer == nil {
		return nil, ErrInvalid
	}
	origin, err := url.Parse(strings.TrimSpace(webOrigin))
	if err != nil || origin.Scheme == "" || origin.Host == "" ||
		(origin.Scheme != "http" && origin.Scheme != "https") || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalid
	}
	origin.Path = strings.TrimRight(origin.Path, "/") + "/"
	return &Preparer{loader: databaseRecipientLoader{db: db}, authorizer: authorizer,
		webOrigin: origin, policy: policy}, nil
}

func (p *Preparer) Prepare(ctx context.Context, delivery emaildelivery.Delivery) (emaildelivery.Message, error) {
	if p == nil || p.loader == nil || p.authorizer == nil || p.webOrigin == nil ||
		delivery.Kind != emaildelivery.KindMention || delivery.ID == uuid.Nil ||
		delivery.RecipientUserID == uuid.Nil || delivery.OrganizationID == nil ||
		delivery.RepositoryID == nil || delivery.CommentID == nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	scope := models.RepoScope{OrgID: *delivery.OrganizationID, RepoID: *delivery.RepositoryID}
	snapshot, err := decodeSnapshot(delivery.Snapshot)
	if err != nil || snapshot.Validate(scope, *delivery.CommentID) != nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	address, err := p.loader.loadRecipient(ctx, delivery.RecipientUserID)
	if err != nil {
		return emaildelivery.Message{}, err
	}
	if !p.policy.Allows(address) {
		return emaildelivery.Message{}, emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	principal := serverauth.Principal{Kind: serverauth.CredentialSession,
		User: serverauth.User{ID: delivery.RecipientUserID, Status: string(models.UserStatusActive)}}
	decision, err := p.authorizer.EvaluateRepository(ctx, authz.Authenticated(principal),
		authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return emaildelivery.Message{}, emaildelivery.Retryable(emaildelivery.ReasonPolicyUnavailable)
	}
	if !decision.Allowed {
		return emaildelivery.Message{}, emaildelivery.Suppressed(emaildelivery.ReasonPolicyUnavailable)
	}
	link := p.commentURL(snapshot)
	actor := cleanLine(snapshot.ActorDisplayName, 80)
	if actor == "" || strings.EqualFold(actor, snapshot.ActorLogin) {
		actor = "@" + snapshot.ActorLogin
	} else {
		actor += " (@" + snapshot.ActorLogin + ")"
	}
	subject := cleanLine(fmt.Sprintf("Mentioned in %s/%s issue #%d",
		snapshot.Organization, snapshot.Repository, snapshot.IssueNumber), 180)
	body := fmt.Sprintf("%s mentioned you in %s/%s issue #%d: %s\n\n%s\n",
		actor, snapshot.Organization, snapshot.Repository, snapshot.IssueNumber,
		cleanLine(snapshot.IssueTitle, 256), link)
	if snapshot.Excerpt != "" {
		body += "\nComment excerpt:\n" + snapshot.Excerpt + "\n"
	}
	return emaildelivery.Message{DeliveryID: delivery.ID, To: address, Subject: subject,
		Body: body, OccurredAt: snapshot.OccurredAt}, nil
}

func decodeSnapshot(raw json.RawMessage) (Snapshot, error) {
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Snapshot{}, ErrInvalid
	}
	return snapshot, nil
}

func (p *Preparer) commentURL(snapshot Snapshot) string {
	path := fmt.Sprintf("%s/%s/issues/%d", url.PathEscape(snapshot.Organization),
		url.PathEscape(snapshot.Repository), snapshot.IssueNumber)
	target, _ := url.Parse(path)
	target.Fragment = fmt.Sprintf("issuecomment-%d", snapshot.CommentNumericID)
	return p.webOrigin.ResolveReference(target).String()
}

func cleanLine(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
