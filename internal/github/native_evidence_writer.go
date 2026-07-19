package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NativeEvidenceWriterOperations is the read-only native Server boundary used
// by Runner preflight. It cannot create, activate, or deactivate assignments.
type NativeEvidenceWriterOperations interface {
	GetNativeEvidenceWriterStatus(context.Context, string) (NativeEvidenceWriterStatus, error)
}

type NativeEvidenceWriterStatus struct {
	UserID string `json:"user_id"`
	Login  string `json:"login"`
	Active bool   `json:"active"`
}

type NativeReviewEvidenceInput struct {
	OrganizationID, RepositoryID, IssueID     string
	ProviderKey, ExternalRepository, ChangeID string
	IngestKey, SubjectRevision                string
	FindingID, ProcessID, SpecID              string
	Path, Side, Severity, Message             string
	Line                                      int
	ReceiptID, ReceiptDigest                  string
}

type NativeReviewEvidenceResult struct {
	EvidenceID string `json:"evidence_id"`
	IngestKey  string `json:"ingest_key"`
	Replayed   bool   `json:"replayed"`
}

type NativeReviewEvidenceOperations interface {
	AppendNativeReviewEvidence(context.Context, string, int, NativeReviewEvidenceInput) (NativeReviewEvidenceResult, error)
}

// GetNativeEvidenceWriterStatus resolves owner/name through the authenticated
// native repository context before reading the current identity's assignment.
// The two responses must agree on one exact UUID tenant scope.
func (c *Client) GetNativeEvidenceWriterStatus(ctx context.Context, repository string) (NativeEvidenceWriterStatus, error) {
	orgID, repoID, err := c.nativeRepositoryScope(ctx, repository)
	if err != nil {
		return NativeEvidenceWriterStatus{}, err
	}
	var result NativeEvidenceWriterStatus
	path := fmt.Sprintf("/orgs/%s/repos/%s/evidence/writers/me", orgID, repoID)
	_, err = c.doRunnerJSON(ctx, http.MethodGet, path, nil, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeEvidenceWriterStatus{}, err
	}
	if userID, err := uuid.Parse(strings.TrimSpace(result.UserID)); err != nil || userID == uuid.Nil || strings.TrimSpace(result.Login) == "" {
		return NativeEvidenceWriterStatus{}, errors.New("native evidence writer status response is incomplete")
	}
	return result, nil
}

func (c *Client) nativeRepositoryScope(ctx context.Context, repository string) (uuid.UUID, uuid.UUID, error) {
	parsed, err := ParseRepo(repository)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	parts := strings.Split(strings.TrimSpace(repository), "/")
	var repositoryContext struct {
		Organization struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"organization"`
		Repository struct {
			Repository NativeRepositorySummary `json:"repository"`
		} `json:"repository"`
		Authenticated bool `json:"authenticated"`
	}
	_, err = c.doRunnerJSON(ctx, http.MethodGet, "/context/repos/"+parsed, nil, nil, ConditionalRequest{}, false, &repositoryContext)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	orgID, orgErr := uuid.Parse(strings.TrimSpace(repositoryContext.Organization.ID))
	repoID, repoErr := uuid.Parse(strings.TrimSpace(repositoryContext.Repository.Repository.ID))
	returnedOrgID, returnedOrgErr := uuid.Parse(strings.TrimSpace(repositoryContext.Repository.Repository.OrganizationID))
	if !repositoryContext.Authenticated || orgErr != nil || repoErr != nil || returnedOrgErr != nil ||
		orgID == uuid.Nil || repoID == uuid.Nil || returnedOrgID != orgID ||
		!strings.EqualFold(strings.TrimSpace(repositoryContext.Organization.Name), parts[0]) ||
		!strings.EqualFold(strings.TrimSpace(repositoryContext.Repository.Repository.Name), parts[1]) {
		return uuid.Nil, uuid.Nil, errors.New("native repository context response is incomplete or mismatched")
	}
	return orgID, repoID, nil
}

type nativeReviewEvidence struct {
	ID                   string `json:"id"`
	IngestKey            string `json:"ingest_key"`
	ProviderKey          string `json:"provider_key"`
	ExternalRepositoryID string `json:"external_repository_id"`
	EvidenceType         string `json:"evidence_type"`
	ExternalID           string `json:"external_id"`
	NormalizedState      string `json:"normalized_state"`
	SubjectRevision      string `json:"subject_revision"`
}

func (c *Client) observeNativeReviewEvidence(ctx context.Context, orgID, repoID, issueID uuid.UUID,
	input NativeReviewEvidenceInput) (nativeReviewEvidence, bool, error) {
	query := url.Values{"provider_key": {input.ProviderKey}, "external_repository_id": {input.ExternalRepository},
		"subject_revision": {input.SubjectRevision}, "evidence_type": {"review"}}
	var envelope struct {
		Evidence []nativeReviewEvidence `json:"evidence"`
	}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/evidence", orgID, repoID, issueID)
	if _, err := c.doRunnerJSON(ctx, http.MethodGet, path, query, nil, ConditionalRequest{}, false, &envelope); err != nil {
		return nativeReviewEvidence{}, false, err
	}
	for _, item := range envelope.Evidence {
		if item.IngestKey != input.IngestKey {
			continue
		}
		if item.ID == "" || item.ProviderKey != input.ProviderKey || item.ExternalRepositoryID != input.ExternalRepository ||
			item.EvidenceType != "review" || item.ExternalID != input.FindingID || item.NormalizedState != "open" ||
			item.SubjectRevision != input.SubjectRevision {
			return nativeReviewEvidence{}, false, errors.New("native review evidence replay identity mismatch")
		}
		return item, true, nil
	}
	return nativeReviewEvidence{}, false, nil
}

func (c *Client) AppendNativeReviewEvidence(ctx context.Context, repository string, issueNumber int,
	input NativeReviewEvidenceInput) (NativeReviewEvidenceResult, error) {
	if _, err := ParseRepo(repository); err != nil || issueNumber <= 0 || input.ProviderKey == "" || input.ExternalRepository == "" || input.ChangeID == "" ||
		input.IngestKey == "" || input.SubjectRevision == "" || input.FindingID == "" || input.ProcessID == "" ||
		input.SpecID == "" || input.Path == "" || input.Line <= 0 || input.Message == "" {
		return NativeReviewEvidenceResult{}, errors.New("native review evidence identity is incomplete")
	}
	orgID, orgErr := uuid.Parse(strings.TrimSpace(input.OrganizationID))
	repoID, repoErr := uuid.Parse(strings.TrimSpace(input.RepositoryID))
	issueID, issueErr := uuid.Parse(strings.TrimSpace(input.IssueID))
	if orgErr != nil || repoErr != nil || issueErr != nil || orgID == uuid.Nil || repoID == uuid.Nil || issueID == uuid.Nil {
		return NativeReviewEvidenceResult{}, errors.New("authoritative native review evidence scope is incomplete")
	}
	if existing, found, observeErr := c.observeNativeReviewEvidence(ctx, orgID, repoID, issueID, input); observeErr != nil {
		return NativeReviewEvidenceResult{}, observeErr
	} else if found {
		return NativeReviewEvidenceResult{EvidenceID: existing.ID, IngestKey: input.IngestKey, Replayed: true}, nil
	}
	payload := map[string]any{"schema_version": "issue-spec.evidence/v1", "change_id": input.ChangeID,
		"severity": input.Severity, "finding_id": input.FindingID, "process_id": input.ProcessID,
		"spec_id": input.SpecID, "summary": input.Message, "path": input.Path, "line": input.Line}
	provenance := map[string]any{"source": "review-submit", "receipt_id": input.ReceiptID,
		"receipt_digest": input.ReceiptDigest, "review_side": input.Side}
	request := map[string]any{"provider_key": input.ProviderKey, "external_repository_id": input.ExternalRepository,
		"evidence_type": "review", "external_id": input.FindingID, "ingest_key": input.IngestKey,
		"normalized_state": "open", "subject_revision": input.SubjectRevision,
		"observed_at": time.Now().UTC().Truncate(time.Microsecond), "payload": payload,
		"provenance": provenance, "visibility": "repository"}
	path := fmt.Sprintf("/orgs/%s/repos/%s/issues/%s/evidence", orgID, repoID, issueID)
	var created nativeReviewEvidence
	_, postErr := c.doRunnerJSON(ctx, http.MethodPost, path, nil, request, ConditionalRequest{}, false, &created)
	if postErr != nil {
		if existing, found, _ := c.observeNativeReviewEvidence(ctx, orgID, repoID, issueID, input); found {
			return NativeReviewEvidenceResult{EvidenceID: existing.ID, IngestKey: input.IngestKey, Replayed: true}, nil
		}
		return NativeReviewEvidenceResult{}, postErr
	}
	if created.ID == "" || created.IngestKey != input.IngestKey || created.ProviderKey != input.ProviderKey ||
		created.ExternalRepositoryID != input.ExternalRepository || created.ExternalID != input.FindingID ||
		created.NormalizedState != "open" || created.SubjectRevision != input.SubjectRevision {
		return NativeReviewEvidenceResult{}, errors.New("native review evidence response identity mismatch")
	}
	return NativeReviewEvidenceResult{EvidenceID: created.ID, IngestKey: input.IngestKey}, nil
}

var _ NativeEvidenceWriterOperations = (*Client)(nil)
var _ NativeReviewEvidenceOperations = (*Client)(nil)
