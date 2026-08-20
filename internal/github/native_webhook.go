package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

type NativeWebhookVerificationOperations interface {
	VerifyNativeRunnerSubscription(context.Context, uuid.UUID, uuid.UUID) (NativeWebhookSubscription, error)
}

type NativeWebhookSubscription struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	RepositoryID   *uuid.UUID `json:"repository_id"`
	ScopeType      string     `json:"scope_type"`
	Active         bool       `json:"active"`
	RevokedAt      *string    `json:"revoked_at"`
	EventTypes     []string   `json:"event_types"`
	DeliveryFormat string     `json:"delivery_format"`
	SigningMode    string     `json:"signing_mode"`
}

func (c *Client) VerifyNativeRunnerSubscription(ctx context.Context, organizationID,
	subscriptionID uuid.UUID) (NativeWebhookSubscription, error) {
	if organizationID == uuid.Nil || subscriptionID == uuid.Nil {
		return NativeWebhookSubscription{}, errors.New("organization and webhook subscription ids are required")
	}
	var result NativeWebhookSubscription
	path := "/orgs/" + url.PathEscape(organizationID.String()) + "/webhooks/" +
		url.PathEscape(subscriptionID.String()) + "/runner-verification"
	_, err := c.doRunnerJSON(ctx, http.MethodPost, path, nil, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeWebhookSubscription{}, err
	}
	if result.ID == uuid.Nil || result.OrganizationID == uuid.Nil || strings.TrimSpace(result.ScopeType) == "" ||
		strings.TrimSpace(result.DeliveryFormat) == "" || strings.TrimSpace(result.SigningMode) == "" || result.EventTypes == nil {
		return NativeWebhookSubscription{}, errors.New("native webhook subscription response is incomplete")
	}
	return result, nil
}

var _ NativeWebhookVerificationOperations = (*Client)(nil)
