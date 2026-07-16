package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/codereview"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidencePolicyWriterTenantAndPublicationLifecycle(t *testing.T) {
	env := newEvidenceEnvironment(t)
	status, err := env.service.DesignatedWriterStatus(t.Context(), authz.Authenticated(env.writer), env.scope)
	if err != nil || status.UserID != env.writer.User.ID || status.Login != env.writer.User.Login || status.Active {
		t.Fatalf("initial DesignatedWriterStatus() = %+v, %v", status, err)
	}
	freshness := 15 * time.Minute
	policy, err := env.service.SetEvidencePolicy(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "policy"),
		env.scope, SetPolicyInput{ExpectedVersion: 0, Requirements: []Requirement{
			{EvidenceType: "review", Freshness: &freshness}, {EvidenceType: "check"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if policy.RepresentationVersion != 1 || len(policy.Requirements) != 2 ||
		policy.Requirements[1].Freshness == nil || *policy.Requirements[1].Freshness != freshness {
		t.Fatalf("policy = %+v", policy)
	}
	if _, err := env.service.SetEvidencePolicy(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "stale-policy"),
		env.scope, SetPolicyInput{ExpectedVersion: 0, Requirements: policy.Requirements}); !errors.Is(err, adminservice.ErrVersionConflict) {
		t.Fatalf("stale SetEvidencePolicy() error = %v", err)
	}
	if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "cross-writer"),
		env.scope, env.otherUserID, true); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org SetDesignatedWriter() error = %v", err)
	}
	assignment, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "writer"),
		env.scope, env.writer.User.ID, true)
	if err != nil || !assignment.Active {
		t.Fatalf("SetDesignatedWriter() = %+v, %v", assignment, err)
	}
	if designated, err := env.service.IsDesignatedWriter(t.Context(), env.scope, env.writer.User.ID); err != nil || !designated {
		t.Fatalf("IsDesignatedWriter() = %t, %v", designated, err)
	}
	status, err = env.service.DesignatedWriterStatus(t.Context(), authz.Authenticated(env.writer), env.scope)
	if err != nil || !status.Active {
		t.Fatalf("active DesignatedWriterStatus() = %+v, %v", status, err)
	}

	input := env.appendInput("check:1", "abc", VisibilityRepository)
	beforeIssue, beforeRepo := env.evidenceVersions(t)
	first, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "append-1"), env.scope, input)
	if err != nil {
		t.Fatal(err)
	}
	afterIssue, afterRepo := env.evidenceVersions(t)
	if afterIssue != beforeIssue+1 || afterRepo != beforeRepo+1 {
		t.Fatalf("append collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}
	if !strings.Contains(string(first.Payload), "9007199254740993") {
		t.Fatalf("large JSON integer changed: %s", first.Payload)
	}
	beforeIssue, beforeRepo = env.evidenceVersions(t)
	retry := input
	retry.Payload = []byte(`{"large":9007199254740993,"state":"passed"}`)
	second, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "append-1"), env.scope, retry)
	if err != nil || second.ID != first.ID {
		t.Fatalf("idempotent AppendEvidence() = %+v, %v", second, err)
	}
	afterIssue, afterRepo = env.evidenceVersions(t)
	if beforeIssue != afterIssue || beforeRepo != afterRepo {
		t.Fatalf("idempotent append bumped collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}
	mismatch := input
	mismatch.Payload = []byte(`{"large":9007199254740994,"state":"passed"}`)
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "append-1"), env.scope, mismatch); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched AppendEvidence() error = %v", err)
	}

	hiddenInput := env.appendInput("review:hidden", "abc", VisibilityMaintainers)
	hiddenInput.EvidenceType = "review"
	hiddenInput.ExternalID = "review-1"
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "hidden"), env.scope, hiddenInput); err != nil {
		t.Fatal(err)
	}
	readerItems, err := env.service.ExactRevision(t.Context(), authz.Authenticated(env.reader), env.scope, ExactRevisionQuery{
		IssueID: env.issueID, ProviderKey: "github", ExternalRepositoryID: "acme/widgets", SubjectRevision: "abc",
	})
	if err != nil || len(readerItems) != 1 || string(readerItems[0].Payload) != string(first.Payload) ||
		string(readerItems[0].Provenance) != string(first.Provenance) {
		t.Fatalf("reader exact evidence = %+v, %v", readerItems, err)
	}
	ownerItems, err := env.service.ExactRevision(t.Context(), authz.Authenticated(env.owner), env.scope, ExactRevisionQuery{
		IssueID: env.issueID, ProviderKey: "github", ExternalRepositoryID: "acme/widgets", SubjectRevision: "abc",
	})
	if err != nil || len(ownerItems) != 2 || ownerItems[0].Payload == nil || ownerItems[0].Provenance == nil {
		t.Fatalf("owner exact evidence = %+v, %v", ownerItems, err)
	}

	superseding := env.appendInput("check:2", "def", VisibilityRepository)
	superseding.SupersedesEvidenceID = &first.ID
	next, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "append-2"), env.scope, superseding)
	if err != nil || next.SupersedesEvidenceID == nil || *next.SupersedesEvidenceID != first.ID {
		t.Fatalf("superseding AppendEvidence() = %+v, %v", next, err)
	}
	branch := env.appendInput("check:branch", "ghi", VisibilityRepository)
	branch.SupersedesEvidenceID = &first.ID
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "branch"), env.scope, branch); !errors.Is(err, adminservice.ErrConflict) {
		t.Fatalf("branching supersedes error = %v", err)
	}
	_, err = env.pool.Exec(t.Context(), `UPDATE external_evidence SET normalized_state = 'failed' WHERE id = $1`, first.ID)
	requireEvidencePGCode(t, err, "55000")
}

func TestEvidenceAuthorizationMatrixAllowsBroadCapsAndRejectsMissingGates(t *testing.T) {
	env := newEvidenceEnvironment(t)
	if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "writer"), env.scope, env.writer.User.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, principal := range []serverauth.Principal{env.undesignated, env.missingScope, env.readerEvidence, env.unrestricted} {
		if principal.User.ID != env.undesignated.User.ID {
			if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "designate-"+principal.User.ID.String()), env.scope, principal.User.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.unrestricted), env.actor(env.unrestricted, "unrestricted"), env.scope,
		env.appendInput("unrestricted", "allowed", VisibilityRepository)); err != nil {
		t.Fatalf("unrestricted PAT evidence error = %v", err)
	}
	otherRepoID := evidenceRepo(t, env.pool, env.scope.OrgID, "multi-cap-repo")
	multiRepository, multiToken := authenticatedEvidencePAT(t, env.pool, env.writer.User.ID,
		[]string{"evidence:write", "issues:read"}, []models.RepoScope{env.scope, {OrgID: env.scope.OrgID, RepoID: otherRepoID}})
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(multiRepository), env.actor(multiRepository, "multi-repository"), env.scope,
		env.appendInput("multi-repository", "allowed", VisibilityRepository)); err != nil {
		t.Fatalf("multi-repository PAT evidence error = %v", err)
	}
	if _, err := env.pool.Exec(t.Context(), `DELETE FROM pat_repositories
		WHERE personal_access_token_id = $1 AND organization_id = $2 AND repository_id = $3`,
		multiRepository.CredentialID, env.scope.OrgID, env.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	withoutTarget, err := pat.New(env.pool, evidenceSecrets(t)).AuthenticateBearer(t.Context(), multiToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(withoutTarget), env.actor(withoutTarget, "target-cap-removed"), env.scope,
		env.appendInput("target-cap-removed", "denied", VisibilityRepository)); !errors.Is(err, adminservice.ErrForbidden) {
		t.Fatalf("removed target cap evidence error = %v", err)
	}
	beforeIssue, beforeRepo := env.evidenceVersions(t)
	principals := []serverauth.Principal{env.undesignated, env.missingScope, env.readerEvidence}
	for i, principal := range principals {
		input := env.appendInput(fmt.Sprintf("denied:%d", i), "denied", VisibilityRepository)
		if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(principal), env.actor(principal, fmt.Sprintf("deny-%d", i)), env.scope, input); !errors.Is(err, adminservice.ErrForbidden) {
			t.Errorf("gate %d error = %v, want forbidden", i, err)
		}
	}
	wrongScope := models.RepoScope{OrgID: env.otherOrgID, RepoID: env.scope.RepoID}
	if _, err := env.service.AppendEvidence(t.Context(), authz.Authenticated(env.writer), env.actor(env.writer, "cross-org"), wrongScope,
		env.appendInput("cross-org", "denied", VisibilityRepository)); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org AppendEvidence() error = %v, want not found", err)
	}
	afterIssue, afterRepo := env.evidenceVersions(t)
	if beforeIssue != afterIssue || beforeRepo != afterRepo {
		t.Fatalf("rejected writes changed collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}
	var rejected int
	var unsafe int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (
		WHERE metadata ? 'payload' OR metadata ? 'provenance' OR metadata ? 'token'
	) FROM audit_events WHERE action = 'external_evidence.publish_rejected'
	AND metadata ? 'target_organization_id' AND metadata ? 'target_repository_id'
	AND metadata - 'reason' - 'operation' - 'target_organization_id' - 'target_repository_id' = '{}'::jsonb`).Scan(&rejected, &unsafe); err != nil {
		t.Fatal(err)
	}
	if rejected != 5 || unsafe != 0 {
		t.Fatalf("rejected audits=%d unsafe=%d, want 5/0", rejected, unsafe)
	}
	var evidenceRows int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FROM external_evidence`).Scan(&evidenceRows); err != nil || evidenceRows != 2 {
		t.Fatalf("evidence rows=%d, %v", evidenceRows, err)
	}
}

func TestDesignatedWriterStatusTracksActivationAndRevocation(t *testing.T) {
	env := newEvidenceEnvironment(t)
	status, err := env.service.DesignatedWriterStatus(t.Context(), authz.Authenticated(env.writer), env.scope)
	if err != nil || status.Active || status.UserID != env.writer.User.ID || status.Login != env.writer.User.Login {
		t.Fatalf("initial status = %+v, %v", status, err)
	}
	if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner),
		env.actor(env.owner, "activate-writer"), env.scope, env.writer.User.ID, true); err != nil {
		t.Fatal(err)
	}
	status, err = env.service.DesignatedWriterStatus(t.Context(), authz.Authenticated(env.writer), env.scope)
	if err != nil || !status.Active {
		t.Fatalf("active status = %+v, %v", status, err)
	}
	if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner),
		env.actor(env.owner, "deactivate-writer"), env.scope, env.writer.User.ID, false); err != nil {
		t.Fatal(err)
	}
	status, err = env.service.DesignatedWriterStatus(t.Context(), authz.Authenticated(env.writer), env.scope)
	if err != nil || status.Active {
		t.Fatalf("revoked status = %+v, %v", status, err)
	}
}

func TestConcurrentEvidenceRetryCreatesExactlyOneRow(t *testing.T) {
	env := newEvidenceEnvironment(t)
	if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner), env.actor(env.owner, "writer"), env.scope, env.writer.User.ID, true); err != nil {
		t.Fatal(err)
	}
	input := env.appendInput("concurrent", "abc", VisibilityRepository)
	const workers = 12
	ids := make(chan uuid.UUID, workers)
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			item, err := env.service.AppendEvidence(context.Background(), authz.Authenticated(env.writer),
				env.actor(env.writer, "concurrent"), env.scope, input)
			if err != nil {
				errs <- err
				return
			}
			ids <- item.ID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first uuid.UUID
	for id := range ids {
		if first == uuid.Nil {
			first = id
		}
		if id != first {
			t.Fatalf("concurrent retry IDs differ: %s / %s", first, id)
		}
	}
	var count int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FROM external_evidence WHERE ingest_key = 'concurrent'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("concurrent evidence count=%d, %v", count, err)
	}
}

func TestProviderSnapshotBatchCASIdempotencyAtomicityAndSupersession(t *testing.T) {
	env := newEvidenceEnvironment(t)
	if _, err := env.service.SetDesignatedWriter(t.Context(), authz.Authenticated(env.owner),
		env.actor(env.owner, "writer"), env.scope, env.writer.User.ID, true); err != nil {
		t.Fatal(err)
	}
	otherRepoID := evidenceRepo(t, env.pool, env.scope.OrgID, "snapshot-multi-cap-repo")
	multiWriter, _ := authenticatedEvidencePAT(t, env.pool, env.writer.User.ID,
		[]string{"evidence:write", "issues:read"}, []models.RepoScope{env.scope, {OrgID: env.scope.OrgID, RepoID: otherRepoID}})
	referenceID := uuid.New()
	if _, err := env.pool.Exec(t.Context(), `INSERT INTO external_references
		(id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		external_repository_id, external_id, canonical_url, metadata)
		VALUES ($1, $2, $3, $4, 'code.example', 'code_change', 'acme/widgets-code', '42',
		'https://code.example/changes/42', '{"head_revision":"abc","base_revision":"base"}'::jsonb)`,
		referenceID, env.scope.OrgID, env.scope.RepoID, env.issueID); err != nil {
		t.Fatal(err)
	}
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets-code", ChangeID: "42"}
	fact := providerFact("check-v1", "ci", "abc", strings.Repeat("a", 64))
	input := SnapshotIngestInput{IssueID: env.issueID, ReferenceID: referenceID, ExpectedReferenceVersion: 1,
		Snapshot: codereview.Snapshot{ProtocolVersion: codereview.ProtocolVersion, Reference: reference,
			SubjectRevision: "abc", Facts: []codereview.ProviderFact{fact},
			CapturedAt: time.Date(2026, 7, 11, 4, 0, 1, 0, time.UTC)}}

	beforeIssue, beforeRepo := env.evidenceVersions(t)
	first, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(multiWriter),
		env.actor(multiWriter, "snapshot-first"), env.scope, input)
	if err != nil || first.Created != 1 || first.Replayed != 0 || len(first.Evidence) != 1 {
		t.Fatalf("first IngestProviderSnapshot() = %+v, %v", first, err)
	}
	afterIssue, afterRepo := env.evidenceVersions(t)
	if afterIssue != beforeIssue+1 || afterRepo != beforeRepo+1 {
		t.Fatalf("batch collection bump issue=%d/%d repo=%d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}
	var ingestKey string
	var provenance json.RawMessage
	if err := env.pool.QueryRow(t.Context(), `SELECT ingest_key, provenance FROM external_evidence WHERE id = $1`,
		first.Evidence[0].ID).Scan(&ingestKey, &provenance); err != nil {
		t.Fatal(err)
	}
	var provenanceObject map[string]any
	if err := json.Unmarshal(provenance, &provenanceObject); err != nil {
		t.Fatalf("decode provenance: %v", err)
	}
	_, hasWriterIdentity := provenanceObject["writer_identity"]
	_, hasTrusted := provenanceObject["trusted"]
	_, hasApproved := provenanceObject["approved"]
	if ingestKey != providerFactIngestKey(referenceID, reference, fact.ID) ||
		provenanceObject["schema_version"] != "issue-spec.provider-fact-provenance/v1" ||
		provenanceObject["provider_fact_id"] != "check-v1" || hasWriterIdentity || hasTrusted || hasApproved {
		t.Fatalf("derived ingest/provenance key=%q provenance=%s", ingestKey, provenance)
	}

	beforeIssue, beforeRepo = env.evidenceVersions(t)
	replay, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-replay"), env.scope, input)
	if err != nil || replay.Created != 0 || replay.Replayed != 1 || replay.Evidence[0].ID != first.Evidence[0].ID {
		t.Fatalf("replay IngestProviderSnapshot() = %+v, %v", replay, err)
	}
	afterIssue, afterRepo = env.evidenceVersions(t)
	if beforeIssue != afterIssue || beforeRepo != afterRepo {
		t.Fatalf("replay bumped collections issue=%d/%d repo=%d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}

	mismatch := input
	mismatch.Snapshot.Facts = append([]codereview.ProviderFact(nil), input.Snapshot.Facts...)
	mismatch.Snapshot.Facts[0].State = "failed"
	if _, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-mismatch"), env.scope, mismatch); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched replay error = %v", err)
	}

	successor := providerFact("check-v2", "ci", "abc", strings.Repeat("b", 64))
	successor.SupersedesID = fact.ID
	nextInput := input
	nextInput.Snapshot.Facts = []codereview.ProviderFact{successor}
	next, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-supersede"), env.scope, nextInput)
	if err != nil || next.Created != 1 || next.Evidence[0].SupersedesEvidenceID == nil ||
		*next.Evidence[0].SupersedesEvidenceID != first.Evidence[0].ID {
		t.Fatalf("superseding snapshot = %+v, %v", next, err)
	}
	nextReplay, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-supersede-replay"), env.scope, nextInput)
	if err != nil || nextReplay.Created != 0 || nextReplay.Replayed != 1 ||
		nextReplay.Evidence[0].ID != next.Evidence[0].ID {
		t.Fatalf("superseding snapshot replay = %+v, %v", nextReplay, err)
	}

	atomicInput := input
	atomicInput.Snapshot.Facts = []codereview.ProviderFact{
		providerFact("atomic-a", "new-a", "abc", strings.Repeat("c", 64)),
		providerFact("atomic-b", "new-b", "abc", strings.Repeat("d", 64)),
	}
	atomicInput.Snapshot.Facts[1].SupersedesID = "missing-predecessor"
	if _, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-atomic"), env.scope, atomicInput); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("atomic snapshot error = %v", err)
	}
	var atomicRows int
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FROM external_evidence WHERE external_id IN ('new-a','new-b')`).Scan(&atomicRows); err != nil || atomicRows != 0 {
		t.Fatalf("partial atomic snapshot rows = %d, %v", atomicRows, err)
	}

	staleVersion := input
	staleVersion.ExpectedReferenceVersion = 2
	if _, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-stale-version"), env.scope, staleVersion); !errors.Is(err, adminservice.ErrVersionConflict) {
		t.Fatalf("stale reference version error = %v", err)
	}
	moved := input
	moved.Snapshot.SubjectRevision = "def"
	moved.Snapshot.Facts = []codereview.ProviderFact{providerFact("moved", "moved", "def", strings.Repeat("e", 64))}
	if _, err := env.service.IngestProviderSnapshot(t.Context(), authz.Authenticated(env.writer),
		env.actor(env.writer, "snapshot-moved"), env.scope, moved); !errors.Is(err, adminservice.ErrVersionConflict) {
		t.Fatalf("moved reference revision error = %v", err)
	}
}

func providerFact(id, externalID, revision, digest string) codereview.ProviderFact {
	return codereview.ProviderFact{ID: id, ExternalID: externalID, Kind: codereview.EvidenceCheck,
		State: "passed", SubjectRevision: revision, Name: "ci", ObservedAt: time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC),
		PayloadDigest: digest}
}

type evidenceEnvironment struct {
	pool           *pgxpool.Pool
	service        *Service
	scope          models.RepoScope
	otherOrgID     uuid.UUID
	otherUserID    uuid.UUID
	issueID        uuid.UUID
	owner          serverauth.Principal
	reader         serverauth.Principal
	writer         serverauth.Principal
	undesignated   serverauth.Principal
	missingScope   serverauth.Principal
	readerEvidence serverauth.Principal
	unrestricted   serverauth.Principal
}

func newEvidenceEnvironment(t *testing.T) evidenceEnvironment {
	t.Helper()
	pool := evidencePool(t)
	authorization, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, authorization)
	if err != nil {
		t.Fatal(err)
	}
	orgID := evidenceOrg(t, pool, "evidence-org")
	otherOrgID := evidenceOrg(t, pool, "other-org")
	repoID := evidenceRepo(t, pool, orgID, "repo")
	scope := models.RepoScope{OrgID: orgID, RepoID: repoID}
	ownerID := evidenceUser(t, pool, "owner")
	readerID := evidenceUser(t, pool, "reader")
	writerID := evidenceUser(t, pool, "writer")
	undesignatedID := evidenceUser(t, pool, "undesignated")
	missingScopeID := evidenceUser(t, pool, "missing-scope")
	readerEvidenceID := evidenceUser(t, pool, "reader-evidence")
	unrestrictedID := evidenceUser(t, pool, "unrestricted")
	otherUserID := evidenceUser(t, pool, "other-user")
	for _, entry := range []struct {
		id   uuid.UUID
		role string
	}{{ownerID, "owner"}, {readerID, "reader"}, {writerID, "maintainer"}, {undesignatedID, "maintainer"},
		{missingScopeID, "maintainer"}, {readerEvidenceID, "reader"}, {unrestrictedID, "maintainer"}} {
		evidenceMembership(t, pool, orgID, entry.id, entry.role)
	}
	evidenceMembership(t, pool, otherOrgID, otherUserID, "maintainer")
	issueID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO issues
		(id, organization_id, repository_id, number, title) VALUES ($1, $2, $3, 1, 'issue')`, issueID, orgID, repoID); err != nil {
		t.Fatal(err)
	}
	return evidenceEnvironment{pool: pool, service: service, scope: scope, otherOrgID: otherOrgID,
		otherUserID: otherUserID, issueID: issueID, owner: evidenceSession(t, pool, ownerID),
		reader: evidenceSession(t, pool, readerID), writer: evidencePAT(t, pool, writerID, scope, []string{"evidence:write", "issues:read"}, true),
		undesignated:   evidencePAT(t, pool, undesignatedID, scope, []string{"evidence:write", "issues:read"}, true),
		missingScope:   evidencePAT(t, pool, missingScopeID, scope, []string{"issues:write"}, true),
		readerEvidence: evidencePAT(t, pool, readerEvidenceID, scope, []string{"evidence:write"}, true),
		unrestricted:   evidencePAT(t, pool, unrestrictedID, scope, []string{"evidence:write"}, false)}
}

func (e evidenceEnvironment) actor(principal serverauth.Principal, requestID string) adminservice.Actor {
	return adminservice.ActorFromPrincipal(principal, requestID)
}

func (e evidenceEnvironment) appendInput(key, revision string, visibility Visibility) AppendInput {
	return AppendInput{IssueID: e.issueID, ProviderKey: "github", ExternalRepositoryID: "acme/widgets",
		EvidenceType: "check", ExternalID: "check-1", IngestKey: key, NormalizedState: "passed",
		SubjectRevision: revision, ObservedAt: time.Date(2026, 7, 11, 4, 0, 0, 123000, time.UTC),
		Payload:    []byte(`{"state":"passed","large":9007199254740993}`),
		Provenance: []byte(`{"adapter":"test","delivery_id":"safe"}`), Visibility: visibility}
}

func (e evidenceEnvironment) evidenceVersions(t *testing.T) (int64, int64) {
	t.Helper()
	var issueVersion, repoVersion int64
	if err := e.pool.QueryRow(t.Context(), `SELECT i.evidence_collection_version, r.evidence_collection_version
		FROM issues i JOIN repos r ON r.organization_id = i.organization_id AND r.id = i.repository_id
		WHERE i.organization_id = $1 AND i.repository_id = $2 AND i.id = $3`, e.scope.OrgID, e.scope.RepoID, e.issueID).
		Scan(&issueVersion, &repoVersion); err != nil {
		t.Fatal(err)
	}
	return issueVersion, repoVersion
}

func evidencePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "evidence_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.RunMigrations(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func evidenceUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func evidenceOrg(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, $2, $2, 'none')`, id, name+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func evidenceRepo(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name, visibility)
		VALUES ($1, $2, $3, $3, 'private')`, id, orgID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func evidenceMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`, orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func evidenceSession(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) serverauth.Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		id, userID, "s-"+id.String(), []byte("token-"+id.String()), []byte("csrf-"+id.String())); err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{User: evidencePrincipalUser(t, pool, userID), Kind: serverauth.CredentialSession, CredentialID: id}
}

func evidencePAT(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, scope models.RepoScope, scopes []string, restricted bool) serverauth.Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO personal_access_tokens
		(id, user_id, name, token_prefix, token_hash) VALUES ($1, $2, 'test', $3, $4)`,
		id, userID, "p-"+id.String(), []byte("pat-"+id.String())); err != nil {
		t.Fatal(err)
	}
	for _, value := range scopes {
		if _, err := pool.Exec(t.Context(), `INSERT INTO pat_scopes (personal_access_token_id, scope) VALUES ($1, $2)`, id, value); err != nil {
			t.Fatal(err)
		}
	}
	var caps []serverauth.RepositoryCap
	if restricted {
		if _, err := pool.Exec(t.Context(), `INSERT INTO pat_repositories
			(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`, id, scope.OrgID, scope.RepoID); err != nil {
			t.Fatal(err)
		}
		caps = []serverauth.RepositoryCap{{OrgID: scope.OrgID, RepoID: scope.RepoID}}
	}
	return serverauth.Principal{User: evidencePrincipalUser(t, pool, userID), Kind: serverauth.CredentialPAT,
		CredentialID: id, Scopes: append([]string(nil), scopes...), RepoRestricted: restricted, RepositoryCaps: caps}
}

func authenticatedEvidencePAT(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, scopes []string,
	repositories []models.RepoScope) (serverauth.Principal, string) {
	t.Helper()
	service := pat.New(pool, evidenceSecrets(t))
	created, err := service.Create(t.Context(), userID, pat.CreateInput{
		Name: "evidence-integration", Scopes: scopes, Repositories: repositories,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateBearer(t.Context(), created.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return principal, created.Plaintext
}

func evidenceSecrets(t *testing.T) *serverauth.Secrets {
	t.Helper()
	secrets, err := serverauth.NewSecrets([]byte(strings.Repeat("p", 32)), []byte(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return secrets
}

func evidencePrincipalUser(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) serverauth.User {
	t.Helper()
	var user serverauth.User
	if err := pool.QueryRow(t.Context(), `SELECT id, login, status FROM users WHERE id = $1`, userID).
		Scan(&user.ID, &user.Login, &user.Status); err != nil {
		t.Fatal(err)
	}
	return user
}

func requireEvidencePGCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("PostgreSQL error = %v, want code %s", err, code)
	}
}
