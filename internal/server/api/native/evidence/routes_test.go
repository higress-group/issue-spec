package evidenceapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/evidence"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestEvidenceRouteSetSuccessAndParameterForwarding(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("NewRouteSet() accepted missing dependencies")
	}
	service := &fakeEvidenceService{}
	set, err := NewRouteSet(Dependencies{Service: service, Authenticate: evidenceAuthenticate})
	if err != nil || len(set.Routes) != 7 {
		t.Fatalf("NewRouteSet() = %+v, %v", set, err)
	}
	mux, _ := routeset.NewMux(routeset.Policy{}, set)
	orgID, repoID, issueID, writerID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	base := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String()

	policy := httptest.NewRequest(http.MethodPut, base+"/evidence/policy", strings.NewReader(`{
		"expected_version":3,"requirements":[{"evidence_type":"check","freshness":60000000000}]}`))
	policy.Header.Set("Authorization", "test")
	policyResponse := httptest.NewRecorder()
	mux.ServeHTTP(policyResponse, policy)
	if policyResponse.Code != http.StatusOK || service.policyInput.ExpectedVersion != 3 || len(service.policyInput.Requirements) != 1 {
		t.Fatalf("policy response=%d input=%+v body=%s", policyResponse.Code, service.policyInput, policyResponse.Body.String())
	}

	writer := httptest.NewRequest(http.MethodPut, base+"/evidence/writers/"+writerID.String(), strings.NewReader(`{"active":true}`))
	writer.Header.Set("Authorization", "test")
	writerResponse := httptest.NewRecorder()
	mux.ServeHTTP(writerResponse, writer)
	if writerResponse.Code != http.StatusOK || service.writerID != writerID || !service.writerActive {
		t.Fatalf("writer response=%d id=%s active=%t", writerResponse.Code, service.writerID, service.writerActive)
	}

	service.writerStatus = evidence.WriterStatus{UserID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Login: "runner", Active: true}
	status := httptest.NewRequest(http.MethodGet, base+"/evidence/writers/me", nil)
	status.Header.Set("Authorization", "test")
	statusResponse := httptest.NewRecorder()
	mux.ServeHTTP(statusResponse, status)
	if statusResponse.Code != http.StatusOK || service.statusScope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) ||
		!strings.Contains(statusResponse.Body.String(), `"login":"runner"`) || !strings.Contains(statusResponse.Body.String(), `"active":true`) {
		t.Fatalf("writer status response=%d scope=%+v body=%s", statusResponse.Code, service.statusScope, statusResponse.Body.String())
	}

	appendPath := base + "/issues/" + issueID.String() + "/evidence"
	append := httptest.NewRequest(http.MethodPost, appendPath, strings.NewReader(`{
		"provider_key":"github","external_repository_id":"acme/widgets","evidence_type":"check",
		"ingest_key":"delivery:1","normalized_state":"passed","subject_revision":"abc",
		"observed_at":"2026-07-11T04:00:00Z","payload":{"safe":true},"provenance":{"adapter":"test"},
		"visibility":"repository"}`))
	append.Header.Set("Authorization", "test")
	append.Header.Set("X-Request-ID", "evidence-request")
	appendResponse := httptest.NewRecorder()
	mux.ServeHTTP(appendResponse, append)
	if appendResponse.Code != http.StatusCreated || appendResponse.Header().Get("Cache-Control") != "no-store" ||
		service.appendInput.IssueID != issueID || service.actor.RequestID != "evidence-request" {
		t.Fatalf("append response=%d input=%+v actor=%+v body=%s", appendResponse.Code, service.appendInput, service.actor, appendResponse.Body.String())
	}

	referenceID := uuid.New()
	snapshotPath := appendPath + "/snapshots"
	snapshot := httptest.NewRequest(http.MethodPost, snapshotPath, strings.NewReader(`{
		"reference_id":"`+referenceID.String()+`","expected_reference_version":4,
		"snapshot":{"protocol_version":"issue-spec.code-provider/v1","reference":{"provider_key":"github",
		"external_repository":"acme/widgets","change_id":"42"},"subject_revision":"abc","facts":[{
		"id":"check-v1","external_id":"ci","kind":"check","state":"passed","subject_revision":"abc",
		"name":"ci","observed_at":"2026-07-11T04:00:00Z","payload_digest":"`+strings.Repeat("a", 64)+`"}],
		"captured_at":"2026-07-11T04:00:01Z"}}`))
	snapshot.Header.Set("Authorization", "test")
	snapshotResponse := httptest.NewRecorder()
	mux.ServeHTTP(snapshotResponse, snapshot)
	if snapshotResponse.Code != http.StatusCreated || service.snapshotInput.IssueID != issueID ||
		service.snapshotInput.ReferenceID != referenceID || service.snapshotInput.ExpectedReferenceVersion != 4 ||
		len(service.snapshotInput.Snapshot.Facts) != 1 {
		t.Fatalf("snapshot response=%d input=%+v body=%s", snapshotResponse.Code, service.snapshotInput, snapshotResponse.Body.String())
	}

	exact := httptest.NewRequest(http.MethodGet, appendPath+"?provider_key=github&external_repository_id=acme%2Fwidgets&subject_revision=abc&evidence_type=check", nil)
	exact.Header.Set("Authorization", "test")
	exactResponse := httptest.NewRecorder()
	mux.ServeHTTP(exactResponse, exact)
	if exactResponse.Code != http.StatusOK || service.query.IssueID != issueID || service.query.ExternalRepositoryID != "acme/widgets" ||
		!strings.Contains(exactResponse.Body.String(), `"evidence":[]`) || strings.Contains(exactResponse.Body.String(), "payload") || strings.Contains(exactResponse.Body.String(), "provenance") {
		t.Fatalf("exact response=%d query=%+v body=%s", exactResponse.Code, service.query, exactResponse.Body.String())
	}
}

func TestEvidenceProblemsStrictInputsConcealmentAndNoPayloadEcho(t *testing.T) {
	service := &fakeEvidenceService{}
	set, _ := NewRouteSet(Dependencies{Service: service, Authenticate: evidenceAuthenticate})
	mux, _ := routeset.NewMux(routeset.Policy{}, set)
	base := "/api/v1/orgs/" + uuid.NewString() + "/repos/" + uuid.NewString()
	issuePath := base + "/issues/" + uuid.NewString() + "/evidence"

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, base+"/evidence/policy", nil))
	assertEvidenceProblem(t, unauthorized, http.StatusUnauthorized, "authentication_required")

	invalidUUID := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/nope/repos/"+uuid.NewString()+"/evidence/policy", nil)
	invalidUUID.Header.Set("Authorization", "test")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalidUUID)
	assertEvidenceProblem(t, invalidResponse, http.StatusUnprocessableEntity, "invalid_request")

	for _, body := range []string{`{"unknown":true}`, `{"provider_key":"` + strings.Repeat("x", 1<<20) + `"}`} {
		req := httptest.NewRequest(http.MethodPost, issuePath, strings.NewReader(body))
		req.Header.Set("Authorization", "test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		assertEvidenceProblem(t, response, http.StatusBadRequest, "invalid_json")
	}

	for _, forged := range []string{`"trusted":true`, `"writer_identity":"operator"`, `"approved":true`} {
		body := `{
			"reference_id":"` + uuid.NewString() + `","expected_reference_version":1,
			"snapshot":{"protocol_version":"issue-spec.code-provider/v1","reference":{"provider_key":"github",
			"external_repository":"acme/widgets","change_id":"42"},"subject_revision":"abc","facts":[{
			"id":"check-v1","external_id":"ci","kind":"check","state":"passed","subject_revision":"abc",
			"name":"ci","observed_at":"2026-07-11T04:00:00Z","payload_digest":"` + strings.Repeat("a", 64) + `",` + forged + `}],
			"captured_at":"2026-07-11T04:00:01Z"}}`
		req := httptest.NewRequest(http.MethodPost, issuePath+"/snapshots", strings.NewReader(body))
		req.Header.Set("Authorization", "test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		assertEvidenceProblem(t, response, http.StatusBadRequest, "invalid_json")
	}

	service.validateVisibility = true
	badVisibility := httptest.NewRequest(http.MethodPost, issuePath, strings.NewReader(`{
		"provider_key":"github","external_repository_id":"a/b","evidence_type":"check","ingest_key":"x",
		"normalized_state":"passed","subject_revision":"abc","observed_at":"2026-07-11T04:00:00Z",
		"payload":{},"provenance":{},"visibility":"private"}`))
	badVisibility.Header.Set("Authorization", "test")
	badVisibilityResponse := httptest.NewRecorder()
	mux.ServeHTTP(badVisibilityResponse, badVisibility)
	assertEvidenceProblem(t, badVisibilityResponse, http.StatusUnprocessableEntity, "invalid_request")
	service.validateVisibility = false

	service.err = adminservice.ErrVersionConflict
	conflict := httptest.NewRequest(http.MethodPut, base+"/evidence/policy", strings.NewReader(`{"expected_version":1,"requirements":[]}`))
	conflict.Header.Set("Authorization", "test")
	conflictResponse := httptest.NewRecorder()
	mux.ServeHTTP(conflictResponse, conflict)
	assertEvidenceProblem(t, conflictResponse, http.StatusConflict, "version_conflict")

	for _, test := range []struct {
		err  error
		want int
		code string
	}{{adminservice.ErrNotFound, http.StatusNotFound, "not_found"}, {adminservice.ErrForbidden, http.StatusForbidden, "forbidden"}} {
		service.err = test.err
		req := httptest.NewRequest(http.MethodGet, base+"/evidence/policy", nil)
		req.Header.Set("Authorization", "test")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		assertEvidenceProblem(t, response, test.want, test.code)
	}

	service.err = adminservice.ErrForbidden
	secret := "DO_NOT_ECHO_PAYLOAD"
	rejected := httptest.NewRequest(http.MethodPost, issuePath, strings.NewReader(`{
		"provider_key":"github","external_repository_id":"a/b","evidence_type":"check","ingest_key":"reject",
		"normalized_state":"passed","subject_revision":"abc","observed_at":"2026-07-11T04:00:00Z",
		"payload":{"secret":"`+secret+`"},"provenance":{"token":"also-secret"}}`))
	rejected.Header.Set("Authorization", "test")
	rejectedResponse := httptest.NewRecorder()
	mux.ServeHTTP(rejectedResponse, rejected)
	assertEvidenceProblem(t, rejectedResponse, http.StatusForbidden, "forbidden")
	if strings.Contains(rejectedResponse.Body.String(), secret) || strings.Contains(rejectedResponse.Body.String(), "also-secret") {
		t.Fatalf("publish rejection echoed sensitive input: %s", rejectedResponse.Body.String())
	}
}

type fakeEvidenceService struct {
	policyInput        evidence.SetPolicyInput
	writerID           uuid.UUID
	writerActive       bool
	writerStatus       evidence.WriterStatus
	statusScope        models.RepoScope
	appendInput        evidence.AppendInput
	snapshotInput      evidence.SnapshotIngestInput
	query              evidence.ExactRevisionQuery
	actor              adminservice.Actor
	err                error
	validateVisibility bool
}

func (f *fakeEvidenceService) EvidencePolicy(context.Context, authz.Subject, models.RepoScope) (evidence.Policy, error) {
	return evidence.Policy{Requirements: []evidence.Requirement{}}, f.err
}

func (f *fakeEvidenceService) SetEvidencePolicy(_ context.Context, _ authz.Subject, actor adminservice.Actor, _ models.RepoScope, input evidence.SetPolicyInput) (evidence.Policy, error) {
	f.policyInput, f.actor = input, actor
	return evidence.Policy{RepresentationVersion: input.ExpectedVersion + 1, Requirements: input.Requirements}, f.err
}

func (f *fakeEvidenceService) DesignatedWriterStatus(_ context.Context, _ authz.Subject, scope models.RepoScope) (evidence.WriterStatus, error) {
	f.statusScope = scope
	return f.writerStatus, f.err
}

func (f *fakeEvidenceService) SetDesignatedWriter(_ context.Context, _ authz.Subject, actor adminservice.Actor, _ models.RepoScope, userID uuid.UUID, active bool) (evidence.WriterAssignment, error) {
	f.writerID, f.writerActive, f.actor = userID, active, actor
	return evidence.WriterAssignment{ID: uuid.New(), UserID: userID, Active: active}, f.err
}

func (f *fakeEvidenceService) AppendEvidence(_ context.Context, _ authz.Subject, actor adminservice.Actor, _ models.RepoScope, input evidence.AppendInput) (evidence.Evidence, error) {
	f.appendInput, f.actor = input, actor
	if f.validateVisibility && input.Visibility != evidence.VisibilityRepository && input.Visibility != evidence.VisibilityMaintainers {
		return evidence.Evidence{}, adminservice.ErrInvalidInput
	}
	return evidence.Evidence{ID: uuid.New(), IssueID: input.IssueID, ProviderKey: input.ProviderKey,
		ExternalRepositoryID: input.ExternalRepositoryID, SubjectRevision: input.SubjectRevision, Visibility: input.Visibility,
		ObservedAt: time.Now().UTC()}, f.err
}

func (f *fakeEvidenceService) IngestProviderSnapshot(_ context.Context, _ authz.Subject, actor adminservice.Actor, _ models.RepoScope, input evidence.SnapshotIngestInput) (evidence.SnapshotIngestResult, error) {
	f.snapshotInput, f.actor = input, actor
	return evidence.SnapshotIngestResult{ReferenceID: input.ReferenceID,
		ReferenceVersion: input.ExpectedReferenceVersion, SubjectRevision: input.Snapshot.SubjectRevision,
		Evidence: []evidence.Evidence{}, Created: len(input.Snapshot.Facts)}, f.err
}

func (f *fakeEvidenceService) ExactRevision(_ context.Context, _ authz.Subject, _ models.RepoScope, query evidence.ExactRevisionQuery) ([]evidence.Evidence, error) {
	f.query = query
	return []evidence.Evidence{}, f.err
}

func evidenceAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		principal := serverauth.Principal{User: serverauth.User{ID: uuid.MustParse("33333333-3333-3333-3333-333333333333")}, Kind: serverauth.CredentialPAT}
		next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
	})
}

func assertEvidenceProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	body := response.Body.String()
	if response.Code != status || !strings.Contains(body, `"code":"`+code+`"`) || !strings.Contains(body, `"request_id"`) || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("problem response=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}
