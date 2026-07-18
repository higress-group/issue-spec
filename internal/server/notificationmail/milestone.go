// Package notificationmail owns only the specialized change-milestone
// snapshot and plain-text preparation. Queueing and lifecycle policy remain in
// the transaction projector.
package notificationmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	notifications "github.com/higress-group/issue-spec/internal/server/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
)

const SnapshotVersion = 1

type MilestoneSnapshot struct {
	Version          int                         `json:"version"`
	ActorLogin       string                      `json:"actor_login"`
	ActorDisplayName string                      `json:"actor_display_name"`
	RepositoryOwner  string                      `json:"repository_owner"`
	RepositoryName   string                      `json:"repository_name"`
	ChangeKey        string                      `json:"change_key"`
	Milestone        store.NotificationMilestone `json:"milestone"`
	IssueID          uuid.UUID                   `json:"issue_id"`
	IssueNumber      int64                       `json:"issue_number,omitempty"`
	IssueTitle       string                      `json:"issue_title,omitempty"`
	Content          string                      `json:"content,omitempty"`
	ContentTruncated bool                        `json:"content_truncated,omitempty"`
	OccurredAt       time.Time                   `json:"occurred_at"`
}

func (s MilestoneSnapshot) Validate() error {
	if s.Version != SnapshotVersion || strings.TrimSpace(s.ActorLogin) == "" ||
		strings.TrimSpace(s.RepositoryOwner) == "" || strings.TrimSpace(s.RepositoryName) == "" ||
		strings.TrimSpace(s.ChangeKey) == "" || len(s.ChangeKey) > 200 || !s.Milestone.Valid() ||
		s.IssueID == uuid.Nil || s.OccurredAt.IsZero() || strings.ContainsAny(s.ActorLogin+s.ChangeKey, "\r\n") {
		return emaildelivery.ErrInvalid
	}
	if s.Milestone != store.NotificationMilestoneCompleted &&
		(s.IssueNumber <= 0 || strings.TrimSpace(s.IssueTitle) == "") {
		return emaildelivery.ErrInvalid
	}
	return nil
}

type Preparer struct {
	eligibility notifications.Eligibility
	webOrigin   publicurl.Origin
	policy      emaildelivery.AddressPolicy
}

func NewPreparer(eligibility notifications.Eligibility, webOrigin publicurl.Origin,
	policy emaildelivery.AddressPolicy) (*Preparer, error) {
	if eligibility == nil {
		return nil, emaildelivery.ErrInvalid
	}
	if _, err := webOrigin.URL("/", nil); err != nil {
		return nil, emaildelivery.ErrInvalid
	}
	return &Preparer{eligibility: eligibility, webOrigin: webOrigin, policy: policy}, nil
}

func (p *Preparer) Prepare(ctx context.Context, delivery emaildelivery.Delivery) (emaildelivery.Message, error) {
	if p == nil || delivery.Kind != emaildelivery.KindChangeMilestone || delivery.ID == uuid.Nil ||
		delivery.RecipientUserID == uuid.Nil || delivery.OrganizationID == nil || delivery.RepositoryID == nil ||
		delivery.MilestoneID == nil || *delivery.MilestoneID == uuid.Nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	var snapshot MilestoneSnapshot
	if json.Unmarshal(delivery.Snapshot, &snapshot) != nil || snapshot.Validate() != nil {
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
	if !p.policy.Allows(recipient.Address) {
		return emaildelivery.Message{}, emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	base := publicurl.RepositoryResource(recipient.RepositoryOwner, recipient.RepositoryName)
	path := base.Web() + "/changes/" + url.PathEscape(snapshot.ChangeKey)
	if snapshot.Milestone != store.NotificationMilestoneCompleted {
		path = base.IssueWeb(snapshot.IssueNumber)
	}
	link, err := p.webOrigin.URL(path, nil)
	if err != nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	subject, body := render(snapshot, link)
	return emaildelivery.Message{DeliveryID: delivery.ID, To: recipient.Address,
		Subject: subject, Body: body, OccurredAt: snapshot.OccurredAt}, nil
}

func render(snapshot MilestoneSnapshot, link string) (string, string) {
	repository := cleanLine(snapshot.RepositoryOwner+"/"+snapshot.RepositoryName, 160)
	changeKey := cleanLine(snapshot.ChangeKey, 200)
	milestone := string(snapshot.Milestone)
	subject := cleanLine(fmt.Sprintf("[%s] Change %s reached %s", repository, changeKey, milestone), 180)
	actor := cleanLine(snapshot.ActorDisplayName, 80)
	if actor == "" || strings.EqualFold(actor, snapshot.ActorLogin) {
		actor = "@" + cleanLine(snapshot.ActorLogin, 80)
	} else {
		actor += " (@" + cleanLine(snapshot.ActorLogin, 80) + ")"
	}
	body := fmt.Sprintf("%s moved change %s in %s to %s.\n\n%s\n", actor, changeKey, repository, milestone, link)
	if snapshot.Milestone != store.NotificationMilestoneCompleted {
		body = fmt.Sprintf("%s created the %s artifact for change %s in %s.\n\n#%d %s\n%s\n",
			actor, milestone, changeKey, repository, snapshot.IssueNumber, cleanLine(snapshot.IssueTitle, 256), link)
		if snapshot.Content != "" {
			body += "\nArtifact content:\n" + snapshot.Content + "\n"
			if snapshot.ContentTruncated {
				body += "[content truncated]\n"
			}
		}
	}
	return subject, body
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
