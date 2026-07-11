package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestLegacyExternalEvidenceIdentityFallback(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "legacy-evidence-identity-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	repo := New(pool).Repo(orgID, repoID)
	issue := createIssue(t, repo, "legacy evidence identity")

	appendLegacyEvidence(t, repo, issue.ID, "legacy-fallback")

	var externalRepositoryID string
	if err := pool.QueryRow(t.Context(), `SELECT external_repository_id FROM external_evidence
		WHERE organization_id = $1 AND repository_id = $2 AND ingest_key = 'legacy-fallback'`, orgID, repoID).
		Scan(&externalRepositoryID); err != nil {
		t.Fatal(err)
	}
	want := "legacy:" + repoID.String()
	if externalRepositoryID != want {
		t.Fatalf("external_repository_id = %q, want %q", externalRepositoryID, want)
	}
}

func TestLegacyExternalEvidenceInfersLatestActiveBindingIdentity(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "bound-evidence-identity-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	repo := New(pool).Repo(orgID, repoID)
	issue := createIssue(t, repo, "bound evidence identity")
	if _, err := pool.Exec(t.Context(), `INSERT INTO source_bindings
		(organization_id, repository_id, provider_key, external_repository_id, clone_url,
		 web_url, default_branch, version, active)
		VALUES ($1, $2, 'github', 'old/repo', 'https://example.test/old.git',
		 'https://example.test/old', 'main', 1, false),
		($1, $2, 'github', 'acme/widgets', 'https://example.test/widgets.git',
		 'https://example.test/widgets', 'main', 2, true)`, orgID, repoID); err != nil {
		t.Fatal(err)
	}

	appendLegacyEvidence(t, repo, issue.ID, "active-binding")

	var externalRepositoryID string
	if err := pool.QueryRow(t.Context(), `SELECT external_repository_id FROM external_evidence
		WHERE organization_id = $1 AND repository_id = $2 AND ingest_key = 'active-binding'`, orgID, repoID).
		Scan(&externalRepositoryID); err != nil {
		t.Fatal(err)
	}
	if externalRepositoryID != "acme/widgets" {
		t.Fatalf("external_repository_id = %q, want active binding identity", externalRepositoryID)
	}
}

func TestExternalEvidenceExplicitIdentityIsPreserved(t *testing.T) {
	pool := migratedIntegrationPool(t)
	orgID := insertOrg(t, pool, "explicit-evidence-identity-org")
	repoID := insertRepo(t, pool, orgID, "repo")
	repo := New(pool).Repo(orgID, repoID)
	issue := createIssue(t, repo, "explicit evidence identity")
	if _, err := pool.Exec(t.Context(), `INSERT INTO source_bindings
		(organization_id, repository_id, provider_key, external_repository_id, clone_url,
		 web_url, default_branch, version, active)
		VALUES ($1, $2, 'github', 'active/repo', 'https://example.test/active.git',
		 'https://example.test/active', 'main', 1, true)`, orgID, repoID); err != nil {
		t.Fatal(err)
	}

	explicit := "pinned/repository"
	if _, err := pool.Exec(t.Context(), `INSERT INTO external_evidence
		(organization_id, repository_id, issue_id, provider_key, external_repository_id,
		 evidence_type, ingest_key, normalized_state, subject_revision, observed_at,
		 payload_hash, payload, provenance, writer_identity_key)
		VALUES ($1, $2, $3, 'github', $4, 'check', 'explicit', 'passed', 'abc',
		 clock_timestamp(), decode('01', 'hex'), '{}'::jsonb, '{}'::jsonb, 'service:test')`,
		orgID, repoID, issue.ID, explicit); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := pool.QueryRow(t.Context(), `SELECT external_repository_id FROM external_evidence
		WHERE organization_id = $1 AND repository_id = $2 AND ingest_key = 'explicit'`, orgID, repoID).
		Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatalf("external_repository_id = %q, want explicit %q", got, explicit)
	}
}

func appendLegacyEvidence(t *testing.T, repo RepoStore, issueID uuid.UUID, ingestKey string) {
	t.Helper()
	if _, err := repo.AppendExternalEvidence(t.Context(), models.NewExternalEvidence{
		IssueID: issueID, ProviderKey: "github", EvidenceType: "check", IngestKey: ingestKey,
		NormalizedState: "passed", SubjectRevision: "abc", ObservedAt: time.Now().UTC(),
		Payload: []byte(`{"state":"passed"}`), Provenance: []byte(`{"adapter":"legacy"}`),
		WriterIdentityKey: "service:legacy",
	}); err != nil {
		t.Fatalf("AppendExternalEvidence() error = %v", err)
	}
}
