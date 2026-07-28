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

var _ NativeReviewEvidenceOperations = (*Client)(nil)
