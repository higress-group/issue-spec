// Package evidenceapi exposes evidence policy, writer and immutable ingestion
// routes as one composable native RouteSet.
package evidenceapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/evidence"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Service interface {
	EvidencePolicy(context.Context, authz.Subject, models.RepoScope) (evidence.Policy, error)
	SetEvidencePolicy(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, evidence.SetPolicyInput) (evidence.Policy, error)
	SetDesignatedWriter(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, uuid.UUID, bool) (evidence.WriterAssignment, error)
	AppendEvidence(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, evidence.AppendInput) (evidence.Evidence, error)
	IngestProviderSnapshot(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, evidence.SnapshotIngestInput) (evidence.SnapshotIngestResult, error)
	ExactRevision(context.Context, authz.Subject, models.RepoScope, evidence.ExactRevisionQuery) ([]evidence.Evidence, error)
}

type Dependencies struct {
	Service      Service
	Authenticate adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil {
		return routeset.RouteSet{}, errors.New("native evidence: service and authentication are required")
	}
	h := handlers{service: deps.Service}
	protect := func(handler http.HandlerFunc) http.Handler { return adminapi.WithRequestID(deps.Authenticate(handler)) }
	set := routeset.RouteSet{Name: "native-evidence", Routes: []routeset.Route{
		{Name: "native.evidence.policy.get", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/evidence/policy", Handler: protect(h.policy)},
		{Name: "native.evidence.policy.set", Method: http.MethodPut, Pattern: "/api/v1/orgs/{org}/repos/{repo}/evidence/policy", Handler: protect(h.setPolicy)},
		{Name: "native.evidence.writer.set", Method: http.MethodPut, Pattern: "/api/v1/orgs/{org}/repos/{repo}/evidence/writers/{user}", Handler: protect(h.setWriter)},
		{Name: "native.evidence.exact_revision", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/issues/{issue}/evidence", Handler: protect(h.exactRevision)},
		{Name: "native.evidence.append", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/issues/{issue}/evidence", Handler: protect(h.appendEvidence)},
		{Name: "native.evidence.snapshot.ingest", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/issues/{issue}/evidence/snapshots", Handler: protect(h.ingestSnapshot)},
	}}
	return set, set.Validate()
}

type handlers struct{ service Service }

func (h handlers) policy(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	item, err := h.service.EvidencePolicy(r.Context(), authz.Authenticated(principal), scope)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, item)
}

func (h handlers) setPolicy(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	var input evidence.SetPolicyInput
	if err := adminapi.DecodeJSON(w, r, &input); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	item, err := h.service.SetEvidencePolicy(r.Context(), authz.Authenticated(principal), actor(r, principal), scope, input)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, item)
}

func (h handlers) setWriter(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(r.PathValue("user"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid writer")
		return
	}
	var input struct {
		Active bool `json:"active"`
	}
	if err := adminapi.DecodeJSON(w, r, &input); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	item, err := h.service.SetDesignatedWriter(r.Context(), authz.Authenticated(principal), actor(r, principal), scope, userID, input.Active)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, item)
}

func (h handlers) exactRevision(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	issueID, err := uuid.Parse(r.PathValue("issue"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid issue")
		return
	}
	items, err := h.service.ExactRevision(r.Context(), authz.Authenticated(principal), scope, evidence.ExactRevisionQuery{
		IssueID: issueID, ProviderKey: r.URL.Query().Get("provider_key"), ExternalRepositoryID: r.URL.Query().Get("external_repository_id"),
		SubjectRevision: r.URL.Query().Get("subject_revision"), EvidenceType: r.URL.Query().Get("evidence_type"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []evidence.Evidence{}
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"evidence": items})
}

func (h handlers) appendEvidence(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	issueID, err := uuid.Parse(r.PathValue("issue"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid issue")
		return
	}
	var input evidence.AppendInput
	if err := adminapi.DecodeJSON(w, r, &input); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if input.IssueID != uuid.Nil && input.IssueID != issueID {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Issue mismatch")
		return
	}
	input.IssueID = issueID
	item, err := h.service.AppendEvidence(r.Context(), authz.Authenticated(principal), actor(r, principal), scope, input)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, item)
}

func (h handlers) ingestSnapshot(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	issueID, err := uuid.Parse(r.PathValue("issue"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid issue")
		return
	}
	var input evidence.SnapshotIngestInput
	if err := adminapi.DecodeJSON(w, r, &input); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if input.IssueID != uuid.Nil && input.IssueID != issueID {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Issue mismatch")
		return
	}
	input.IssueID = issueID
	result, err := h.service.IngestProviderSnapshot(r.Context(), authz.Authenticated(principal), actor(r, principal), scope, input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if result.Created == 0 {
		status = http.StatusOK
	}
	adminapi.WriteJSON(w, status, result)
}

func requestScope(w http.ResponseWriter, r *http.Request) (serverauth.Principal, models.RepoScope, bool) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return serverauth.Principal{}, models.RepoScope{}, false
	}
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	if orgErr != nil || repoErr != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository")
		return serverauth.Principal{}, models.RepoScope{}, false
	}
	return principal, models.RepoScope{OrgID: orgID, RepoID: repoID}, true
}

func actor(r *http.Request, principal serverauth.Principal) adminservice.Actor {
	return adminservice.ActorFromPrincipal(principal, adminapi.RequestID(r))
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminservice.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, adminservice.ErrForbidden):
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, adminservice.ErrVersionConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "version_conflict", "Resource version conflict")
	case errors.Is(err, evidence.ErrIdempotencyMismatch):
		adminapi.WriteProblem(w, http.StatusConflict, "idempotency_mismatch", "Idempotency key reused with different content")
	case errors.Is(err, adminservice.ErrConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "conflict", "Resource conflict")
	case errors.Is(err, adminservice.ErrInvalidInput):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}
