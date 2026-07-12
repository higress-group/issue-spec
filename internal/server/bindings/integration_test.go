package bindings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureBindingReusesIdenticalAndConflictsWithoutVersionBump(t *testing.T) {
	env := newBindingsEnvironment(t)
	input := CreateBindingVersionInput{ProviderKey: "code.example", ExternalRepositoryID: "acme/widgets",
		CloneURL: "https://code.example/acme/widgets.git", WebURL: "https://code.example/acme/widgets", DefaultBranch: "main"}
	first, err := env.service.EnsureBinding(t.Context(), authz.Authenticated(env.owner), env.actor("ensure-first"), env.scope, input)
	if err != nil || !first.Created {
		t.Fatalf("first ensure = %+v, %v", first, err)
	}
	second, err := env.service.EnsureBinding(t.Context(), authz.Authenticated(env.owner), env.actor("ensure-second"), env.scope, input)
	if err != nil || second.Created || second.Binding.ID != first.Binding.ID {
		t.Fatalf("second ensure = %+v, %v", second, err)
	}
	conflict := input
	conflict.ExternalRepositoryID = "acme/other"
	if _, err := env.service.EnsureBinding(t.Context(), authz.Authenticated(env.owner), env.actor("ensure-conflict"), env.scope, conflict); !errors.Is(err, adminservice.ErrConflict) {
		t.Fatalf("incompatible ensure error = %v", err)
	}
	var rows, audits int
	var collection int64
	if err := env.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM source_bindings WHERE organization_id=$1 AND repository_id=$2),
		(SELECT count(*) FROM audit_events WHERE organization_id=$1 AND repository_id=$2 AND action='source_binding.ensure.create'),
		(SELECT bindings_collection_version FROM repos WHERE organization_id=$1 AND id=$2)`, env.scope.OrgID, env.scope.RepoID).
		Scan(&rows, &audits, &collection); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || audits != 1 || collection != 2 {
		t.Fatalf("rows=%d audits=%d collection=%d", rows, audits, collection)
	}
}

func TestBindingVersionsSerializeValidateAndDeactivate(t *testing.T) {
	env := newBindingsEnvironment(t)
	const versions = 8
	results := make(chan Binding, versions)
	errorsCh := make(chan error, versions)
	var wait sync.WaitGroup
	for i := 0; i < versions; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			item, err := env.service.CreateBindingVersion(context.Background(), authz.Authenticated(env.owner),
				env.actor(fmt.Sprintf("binding-%d", index)), env.scope, CreateBindingVersionInput{
					ProviderKey: "github", ExternalRepositoryID: fmt.Sprintf("acme/repo-%d", index),
					CloneURL: fmt.Sprintf("https://code.example/acme/repo-%d.git", index),
					WebURL:   fmt.Sprintf("https://code.example/acme/repo-%d", index), DefaultBranch: "main",
				})
			if err != nil {
				errorsCh <- err
				return
			}
			results <- item
		}(i)
	}
	wait.Wait()
	close(errorsCh)
	close(results)
	for err := range errorsCh {
		t.Fatal(err)
	}
	seen := make(map[int64]bool)
	for item := range results {
		seen[item.Version] = true
	}
	for version := int64(1); version <= versions; version++ {
		if !seen[version] {
			t.Fatalf("missing serialized binding version %d: %+v", version, seen)
		}
	}
	active, err := env.service.ActiveBinding(t.Context(), authz.Authenticated(env.owner), env.scope)
	if err != nil || active.Version != versions {
		t.Fatalf("ActiveBinding() = %+v, %v", active, err)
	}
	var activeCount, collection int64
	if err := env.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE active),
		(SELECT bindings_collection_version FROM repos WHERE organization_id = $1 AND id = $2)
		FROM source_bindings WHERE organization_id = $1 AND repository_id = $2`, env.scope.OrgID, env.scope.RepoID).
		Scan(&activeCount, &collection); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 || collection != versions+1 {
		t.Fatalf("active=%d collection=%d, want 1/%d", activeCount, collection, versions+1)
	}

	for _, cloneURL := range []string{
		"https://user@example.test/repo.git", "https://example.test/repo.git?token=x",
		"https://example.test/repo.git#main", "file:///tmp/repo", "git://example.test/repo",
		"git@example.test:repo.git", "ftp://example.test/repo",
	} {
		_, err := env.service.CreateBindingVersion(t.Context(), authz.Authenticated(env.owner), env.actor("invalid"), env.scope,
			CreateBindingVersionInput{ProviderKey: "github", ExternalRepositoryID: "x/y", CloneURL: cloneURL,
				WebURL: "https://example.test/x/y", DefaultBranch: "main"})
		if !errors.Is(err, adminservice.ErrInvalidInput) {
			t.Errorf("clone URL %q error = %v, want invalid input", cloneURL, err)
		}
	}
	if err := env.service.DeactivateBinding(t.Context(), authz.Authenticated(env.owner), env.actor("deactivate"), env.scope); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.ActiveBinding(t.Context(), authz.Authenticated(env.owner), env.scope); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("ActiveBinding() after deactivate error = %v", err)
	}
	if err := env.service.DeactivateBinding(t.Context(), authz.Authenticated(env.owner), env.actor("deactivate-again"), env.scope); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("second DeactivateBinding() error = %v", err)
	}
}

func TestReferencesIdentityVisibilityCollectionsAndTenantIsolation(t *testing.T) {
	env := newBindingsEnvironment(t)
	repository := UpsertReferenceInput{IssueID: env.issueID, ProviderKey: "github", RelationKind: "code-change",
		ExternalRepositoryID: "acme/one", ExternalID: "42", CanonicalURL: "https://code.example/acme/one/pull/42",
		LifecycleState: "open", Visibility: VisibilityRepository, Metadata: []byte(`{"large":9007199254740993}`)}
	first, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner), env.actor("ref-one"), env.scope, repository)
	if err != nil {
		t.Fatal(err)
	}
	other := repository
	other.ExternalRepositoryID = "acme/two"
	other.CanonicalURL = "https://code.example/acme/two/pull/42"
	if _, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner), env.actor("ref-two"), env.scope, other); err != nil {
		t.Fatal(err)
	}
	hidden := repository
	hidden.ExternalID = "43"
	hidden.CanonicalURL = "https://code.example/acme/one/pull/43"
	hidden.Visibility = VisibilityMaintainers
	if _, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner), env.actor("ref-hidden"), env.scope, hidden); err != nil {
		t.Fatal(err)
	}
	beforeIssue, beforeRepo := env.referenceVersions(t)
	retry, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner), env.actor("ref-retry"), env.scope, repository)
	if err != nil || retry.ID != first.ID || retry.RepresentationVersion != first.RepresentationVersion {
		t.Fatalf("idempotent UpsertReference() = %+v, %v", retry, err)
	}
	afterIssue, afterRepo := env.referenceVersions(t)
	if beforeIssue != afterIssue || beforeRepo != afterRepo {
		t.Fatalf("idempotent upsert bumped collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}

	readerItems, err := env.service.ListReferences(t.Context(), authz.Authenticated(env.reader), env.scope, env.issueID)
	if err != nil || len(readerItems) != 2 {
		t.Fatalf("reader references = %+v, %v", readerItems, err)
	}
	for _, item := range readerItems {
		if item.Metadata != nil {
			t.Fatalf("reader received sensitive metadata: %s", item.Metadata)
		}
	}
	ownerItems, err := env.service.ListReferences(t.Context(), authz.Authenticated(env.owner), env.scope, env.issueID)
	if err != nil || len(ownerItems) != 3 || !strings.Contains(string(ownerItems[0].Metadata)+string(ownerItems[1].Metadata)+string(ownerItems[2].Metadata), "9007199254740993") {
		t.Fatalf("owner references = %+v, %v", ownerItems, err)
	}

	beforeIssue, beforeRepo = env.referenceVersions(t)
	if err := env.service.DeleteReference(t.Context(), authz.Authenticated(env.owner), env.actor("ref-delete"), env.scope, env.issueID, first.ID); err != nil {
		t.Fatal(err)
	}
	afterIssue, afterRepo = env.referenceVersions(t)
	if afterIssue != beforeIssue+1 || afterRepo != beforeRepo+1 {
		t.Fatalf("delete collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}
	wrongScope := models.RepoScope{OrgID: env.otherOrgID, RepoID: env.scope.RepoID}
	if _, err := env.service.ListReferences(t.Context(), authz.Authenticated(env.owner), wrongScope, env.issueID); !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("cross-org ListReferences() error = %v, want not found", err)
	}
}

func TestPersistedExternalURLsRejectCredentialsAndReaderFailsClosed(t *testing.T) {
	writeEnv := newBindingsEnvironment(t)
	unsafeURLs := []string{
		"https://code.example/acme/widgets?access_token=BINDING_SECRET",
		"https://code.example/acme/widgets?",
		"https://code.example/acme/widgets#access_token=BINDING_SECRET",
		"https://user:secret@code.example/acme/widgets",
		"https://CODE.example/acme/widgets",
		"https://code.example/acme/../widgets",
		"https://code.example/acme/widgets\nnext",
	}
	for index, unsafeURL := range unsafeURLs {
		_, err := writeEnv.service.CreateBindingVersion(t.Context(), authz.Authenticated(writeEnv.owner),
			writeEnv.actor(fmt.Sprintf("unsafe-binding-%d", index)), writeEnv.scope, CreateBindingVersionInput{
				ProviderKey: "code.example", ExternalRepositoryID: "acme/widgets",
				CloneURL: "https://code.example/acme/widgets.git", WebURL: unsafeURL, DefaultBranch: "main"})
		if !errors.Is(err, adminservice.ErrInvalidInput) {
			t.Errorf("binding WebURL %q error = %v, want invalid input", unsafeURL, err)
		}
		_, err = writeEnv.service.UpsertReference(t.Context(), authz.Authenticated(writeEnv.owner),
			writeEnv.actor(fmt.Sprintf("unsafe-reference-%d", index)), writeEnv.scope, UpsertReferenceInput{
				IssueID: writeEnv.issueID, ProviderKey: "code.example", RelationKind: "code_change",
				ExternalRepositoryID: "acme/widgets", ExternalID: fmt.Sprintf("change-%d", index),
				CanonicalURL: unsafeURL, LifecycleState: "active", Visibility: VisibilityRepository})
		if !errors.Is(err, adminservice.ErrInvalidInput) {
			t.Errorf("reference CanonicalURL %q error = %v, want invalid input", unsafeURL, err)
		}
	}
	var bindingsCount, referencesCount int
	if err := writeEnv.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM source_bindings WHERE organization_id=$1 AND repository_id=$2),
		(SELECT count(*) FROM external_references WHERE organization_id=$1 AND repository_id=$2)`,
		writeEnv.scope.OrgID, writeEnv.scope.RepoID).Scan(&bindingsCount, &referencesCount); err != nil {
		t.Fatal(err)
	}
	if bindingsCount != 0 || referencesCount != 0 {
		t.Fatalf("unsafe URL writes persisted bindings=%d references=%d", bindingsCount, referencesCount)
	}

	// Defense in depth for pre-fix/directly imported rows: a repository reader
	// receives an error, never the persisted token-bearing URL.
	readEnv := newBindingsEnvironment(t)
	const bindingSecret = "LEGACY_BINDING_TOKEN"
	if _, err := readEnv.pool.Exec(t.Context(), `INSERT INTO source_bindings
		(id, organization_id, repository_id, provider_key, external_repository_id, clone_url, web_url,
		 default_branch, version, active) VALUES ($1,$2,$3,'code.example','acme/widgets',
		 'https://code.example/acme/widgets.git',$4,'main',1,true)`, uuid.New(), readEnv.scope.OrgID,
		readEnv.scope.RepoID, "https://code.example/acme/widgets?access_token="+bindingSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnv.service.ActiveBinding(t.Context(), authz.Authenticated(readEnv.reader), readEnv.scope); err == nil || strings.Contains(err.Error(), bindingSecret) {
		t.Fatalf("reader unsafe binding error = %v", err)
	}

	const referenceSecret = "LEGACY_REFERENCE_TOKEN"
	if _, err := readEnv.pool.Exec(t.Context(), `INSERT INTO external_references
		(id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		 external_repository_id, external_id, canonical_url, lifecycle_state, visibility)
		 VALUES ($1,$2,$3,$4,'code.example','code_change','acme/widgets','change-legacy',$5,'active','repository')`,
		uuid.New(), readEnv.scope.OrgID, readEnv.scope.RepoID, readEnv.issueID,
		"https://code.example/changes/legacy?access_token="+referenceSecret); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnv.service.ListReferences(t.Context(), authz.Authenticated(readEnv.reader), readEnv.scope, readEnv.issueID); err == nil || strings.Contains(err.Error(), referenceSecret) {
		t.Fatalf("reader unsafe reference error = %v", err)
	}
}

type bindingsEnvironment struct {
	pool       *pgxpool.Pool
	service    *Service
	scope      models.RepoScope
	otherOrgID uuid.UUID
	issueID    uuid.UUID
	owner      serverauth.Principal
	reader     serverauth.Principal
}

func newBindingsEnvironment(t *testing.T) bindingsEnvironment {
	t.Helper()
	pool := bindingsPool(t)
	authorization, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, authorization)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := insertBindingsUser(t, pool, "owner")
	readerID := insertBindingsUser(t, pool, "reader")
	orgID := insertBindingsOrg(t, pool, "bindings-org")
	otherOrgID := insertBindingsOrg(t, pool, "bindings-other-org")
	repoID := insertBindingsRepo(t, pool, orgID, "repo")
	insertBindingsMembership(t, pool, orgID, ownerID, "owner")
	insertBindingsMembership(t, pool, orgID, readerID, "reader")
	issueID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO issues
		(id, organization_id, repository_id, number, title) VALUES ($1, $2, $3, 1, 'issue')`, issueID, orgID, repoID); err != nil {
		t.Fatal(err)
	}
	return bindingsEnvironment{pool: pool, service: service, scope: models.RepoScope{OrgID: orgID, RepoID: repoID},
		otherOrgID: otherOrgID, issueID: issueID,
		owner: bindingsSession(t, pool, ownerID), reader: bindingsSession(t, pool, readerID)}
}

func (e bindingsEnvironment) actor(requestID string) adminservice.Actor {
	return adminservice.ActorFromPrincipal(e.owner, requestID)
}

func (e bindingsEnvironment) referenceVersions(t *testing.T) (int64, int64) {
	t.Helper()
	var issueVersion, repoVersion int64
	if err := e.pool.QueryRow(t.Context(), `SELECT i.references_collection_version, r.references_collection_version
		FROM issues i JOIN repos r ON r.organization_id = i.organization_id AND r.id = i.repository_id
		WHERE i.organization_id = $1 AND i.repository_id = $2 AND i.id = $3`, e.scope.OrgID, e.scope.RepoID, e.issueID).
		Scan(&issueVersion, &repoVersion); err != nil {
		t.Fatal(err)
	}
	return issueVersion, repoVersion
}

func bindingsPool(t *testing.T) *pgxpool.Pool {
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
	schema := "bindings_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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

func insertBindingsUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertBindingsOrg(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, $2, $2, 'none')`, id, name+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertBindingsRepo(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name, visibility)
		VALUES ($1, $2, $3, $3, 'private')`, id, orgID, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertBindingsMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`,
		orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func bindingsSession(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) serverauth.Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		id, userID, "s-"+id.String(), []byte("token-"+id.String()), []byte("csrf-"+id.String())); err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{User: serverauth.User{ID: userID, Status: "active"}, Kind: serverauth.CredentialSession, CredentialID: id}
}
