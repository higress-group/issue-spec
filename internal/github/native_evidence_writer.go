package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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

// GetNativeEvidenceWriterStatus resolves owner/name through the authenticated
// native repository context before reading the current identity's assignment.
// The two responses must agree on one exact UUID tenant scope.
func (c *Client) GetNativeEvidenceWriterStatus(ctx context.Context, repository string) (NativeEvidenceWriterStatus, error) {
	parsed, err := ParseRepo(repository)
	if err != nil {
		return NativeEvidenceWriterStatus{}, err
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
		return NativeEvidenceWriterStatus{}, err
	}
	orgID, orgErr := uuid.Parse(strings.TrimSpace(repositoryContext.Organization.ID))
	repoID, repoErr := uuid.Parse(strings.TrimSpace(repositoryContext.Repository.Repository.ID))
	returnedOrgID, returnedOrgErr := uuid.Parse(strings.TrimSpace(repositoryContext.Repository.Repository.OrganizationID))
	if !repositoryContext.Authenticated || orgErr != nil || repoErr != nil || returnedOrgErr != nil ||
		orgID == uuid.Nil || repoID == uuid.Nil || returnedOrgID != orgID ||
		!strings.EqualFold(strings.TrimSpace(repositoryContext.Organization.Name), parts[0]) ||
		!strings.EqualFold(strings.TrimSpace(repositoryContext.Repository.Repository.Name), parts[1]) {
		return NativeEvidenceWriterStatus{}, errors.New("native repository context response is incomplete or mismatched")
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

var _ NativeEvidenceWriterOperations = (*Client)(nil)
