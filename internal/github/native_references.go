package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type NativeReferenceOperations interface {
	ListNativeReferences(context.Context, models.RepoScope, uuid.UUID) ([]NativeReference, error)
	UpsertNativeReference(context.Context, models.RepoScope, uuid.UUID, NativeUpsertReferenceInput) (NativeReference, error)
}

type NativeReference struct {
	ID                    string          `json:"id"`
	IssueID               string          `json:"issue_id"`
	ProviderKey           string          `json:"provider_key"`
	RelationKind          string          `json:"relation_kind"`
	ExternalRepositoryID  string          `json:"external_repository_id"`
	ExternalID            string          `json:"external_id"`
	CanonicalURL          string          `json:"canonical_url"`
	Title                 *string         `json:"title,omitempty"`
	LifecycleState        string          `json:"lifecycle_state"`
	Visibility            string          `json:"visibility"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
	RepresentationVersion int64           `json:"representation_version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type NativeUpsertReferenceInput struct {
	ProviderKey          string          `json:"provider_key"`
	RelationKind         string          `json:"relation_kind"`
	ExternalRepositoryID string          `json:"external_repository_id"`
	ExternalID           string          `json:"external_id"`
	CanonicalURL         string          `json:"canonical_url"`
	Title                *string         `json:"title,omitempty"`
	LifecycleState       string          `json:"lifecycle_state"`
	Visibility           string          `json:"visibility"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	Refresh              bool            `json:"refresh,omitempty"`
	ExpectedVersion      *int64          `json:"expected_version,omitempty"`
}

type NativeCodeChangeConflictReason string

const (
	NativeCodeChangeConflictAmbiguousActiveReferences NativeCodeChangeConflictReason = "ambiguous_active_references"
	NativeCodeChangeConflictCanonicalURLDrift         NativeCodeChangeConflictReason = "canonical_url_drift"
	NativeCodeChangeConflictDifferentActiveChange     NativeCodeChangeConflictReason = "different_active_change"
	NativeCodeChangeConflictInvalidActiveReference    NativeCodeChangeConflictReason = "invalid_active_reference"
	NativeCodeChangeConflictRefreshRequired           NativeCodeChangeConflictReason = "refresh_required"
	NativeCodeChangeConflictStaleReferenceVersion     NativeCodeChangeConflictReason = "stale_reference_version"
)

type NativeReferenceIdentity struct {
	ID                    string `json:"id"`
	ProviderKey           string `json:"provider_key"`
	ExternalRepositoryID  string `json:"external_repository_id"`
	ExternalID            string `json:"external_id"`
	RepresentationVersion int64  `json:"representation_version"`
}

type NativeCodeChangeConflictError struct {
	Reason     NativeCodeChangeConflictReason `json:"reason"`
	References []NativeReferenceIdentity      `json:"references"`
	RequestID  string                         `json:"request_id,omitempty"`
	cause      error
}

func (e *NativeCodeChangeConflictError) Error() string {
	return fmt.Sprintf("native active code-change conflict (%s)", e.Reason)
}

func (e *NativeCodeChangeConflictError) Unwrap() error { return e.cause }

func (c *Client) ListNativeReferences(ctx context.Context, scope models.RepoScope, issueID uuid.UUID) ([]NativeReference, error) {
	if err := scope.Validate(); err != nil || issueID == uuid.Nil {
		return nil, errors.New("native reference list scope is invalid")
	}
	var result struct {
		References []NativeReference `json:"references"`
	}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/references", scope.OrgID, scope.RepoID, issueID)
	if _, err := c.doRunnerJSON(ctx, http.MethodGet, path, nil, nil, ConditionalRequest{}, false, &result); err != nil {
		return nil, err
	}
	for _, reference := range result.References {
		if !validNativeReference(reference, issueID) {
			return nil, errors.New("native reference list response is incomplete or invalid")
		}
	}
	return result.References, nil
}

func (c *Client) UpsertNativeReference(ctx context.Context, scope models.RepoScope, issueID uuid.UUID, input NativeUpsertReferenceInput) (NativeReference, error) {
	if err := validateNativeReferenceInput(scope, issueID, input); err != nil {
		return NativeReference{}, err
	}
	var result NativeReference
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/references", scope.OrgID, scope.RepoID, issueID)
	_, err := c.doRunnerJSON(ctx, http.MethodPut, path, nil, input, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeReference{}, nativeReferenceError(err)
	}
	if err := validateNativeReferenceResult(result, issueID, input); err != nil {
		return NativeReference{}, err
	}
	return result, nil
}

func validateNativeReferenceInput(scope models.RepoScope, issueID uuid.UUID, input NativeUpsertReferenceInput) error {
	if err := scope.Validate(); err != nil || issueID == uuid.Nil || !exactNonempty(input.ProviderKey) ||
		!exactNonempty(input.RelationKind) || !exactNonempty(input.ExternalRepositoryID) ||
		!exactNonempty(input.ExternalID) || !exactNonempty(input.LifecycleState) ||
		(input.Visibility != "repository" && input.Visibility != "maintainers") ||
		!nativeReferenceURL(input.CanonicalURL) || !nativeMetadataObject(input.Metadata) ||
		input.Refresh != (input.ExpectedVersion != nil) || (input.ExpectedVersion != nil && *input.ExpectedVersion < 1) {
		return errors.New("native reference upsert input is invalid")
	}
	return nil
}

func validateNativeReferenceResult(result NativeReference, issueID uuid.UUID, input NativeUpsertReferenceInput) error {
	if !validNativeReference(result, issueID) ||
		result.ProviderKey != input.ProviderKey || result.RelationKind != input.RelationKind ||
		result.ExternalRepositoryID != input.ExternalRepositoryID || result.ExternalID != input.ExternalID ||
		result.CanonicalURL != input.CanonicalURL {
		return errors.New("native reference upsert response is incomplete or mismatched")
	}
	return nil
}

func validNativeReference(result NativeReference, issueID uuid.UUID) bool {
	resultID, resultIDErr := uuid.Parse(strings.TrimSpace(result.ID))
	resultIssueID, resultIssueIDErr := uuid.Parse(strings.TrimSpace(result.IssueID))
	return resultIDErr == nil && resultIssueIDErr == nil && resultID != uuid.Nil && resultIssueID == issueID &&
		exactNonempty(result.ProviderKey) && exactNonempty(result.RelationKind) &&
		exactNonempty(result.ExternalRepositoryID) && exactNonempty(result.ExternalID) &&
		nativeReferenceURL(result.CanonicalURL) && exactNonempty(result.LifecycleState) &&
		(result.Visibility == "repository" || result.Visibility == "maintainers") &&
		result.RepresentationVersion >= 1 && nativeMetadataObject(result.Metadata)
}

func nativeReferenceError(err error) error {
	var apiErr *APIError
	if !errorAsAPI(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
		return err
	}
	var problem struct {
		Code      string `json:"code"`
		RequestID string `json:"request_id"`
		Meta      struct {
			Reason     NativeCodeChangeConflictReason `json:"reason"`
			References []NativeReferenceIdentity      `json:"references"`
		} `json:"meta"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &problem) != nil || problem.Code != "code_change_conflict" ||
		!knownNativeCodeChangeConflictReason(problem.Meta.Reason) ||
		!validNativeConflictCardinality(problem.Meta.Reason, len(problem.Meta.References)) {
		return err
	}
	for _, reference := range problem.Meta.References {
		id, parseErr := uuid.Parse(strings.TrimSpace(reference.ID))
		if parseErr != nil || id == uuid.Nil || !exactNonempty(reference.ProviderKey) ||
			!exactNonempty(reference.ExternalRepositoryID) || !exactNonempty(reference.ExternalID) ||
			reference.RepresentationVersion < 1 {
			return err
		}
	}
	return &NativeCodeChangeConflictError{Reason: problem.Meta.Reason, References: problem.Meta.References,
		RequestID: strings.TrimSpace(problem.RequestID), cause: err}
}

func validNativeConflictCardinality(reason NativeCodeChangeConflictReason, count int) bool {
	if reason == NativeCodeChangeConflictAmbiguousActiveReferences {
		return count >= 2
	}
	return count == 1
}

func knownNativeCodeChangeConflictReason(reason NativeCodeChangeConflictReason) bool {
	switch reason {
	case NativeCodeChangeConflictAmbiguousActiveReferences, NativeCodeChangeConflictCanonicalURLDrift,
		NativeCodeChangeConflictDifferentActiveChange, NativeCodeChangeConflictInvalidActiveReference,
		NativeCodeChangeConflictRefreshRequired, NativeCodeChangeConflictStaleReferenceVersion:
		return true
	default:
		return false
	}
}

func exactNonempty(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func nativeMetadataObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func nativeReferenceURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && raw != "" && raw == strings.TrimSpace(raw) && parsed.IsAbs() && parsed.Scheme == "https" &&
		parsed.Host != "" && parsed.User == nil && parsed.Opaque == "" && parsed.RawQuery == "" && !parsed.ForceQuery &&
		parsed.Fragment == "" && parsed.RawFragment == "" && parsed.String() == raw
}

var _ NativeReferenceOperations = (*Client)(nil)
