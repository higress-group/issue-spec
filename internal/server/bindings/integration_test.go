package bindings

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
		if !strings.Contains(string(item.Metadata), "9007199254740993") {
			t.Fatalf("reader did not receive repository-visible metadata: %s", item.Metadata)
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

func TestImplementCodeChangeLifecycleExactRetryAndConditionalRefresh(t *testing.T) {
	env := newBindingsEnvironment(t)
	env.markImplement(t)
	input := env.codeChangeInput("42", "abc123")
	missingRevision := input
	missingRevision.Metadata = json.RawMessage(`{}`)
	if err := env.upsertReference(t, "code-change-missing-revision", missingRevision); !errors.Is(err, adminservice.ErrInvalidInput) {
		t.Fatalf("missing head revision error = %v, want invalid input", err)
	}
	first, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner),
		env.actor("code-change-first"), env.scope, input)
	if err != nil || first.RepresentationVersion != 1 {
		t.Fatalf("first code-change = %+v, %v", first, err)
	}
	beforeIssue, beforeRepo := env.referenceVersions(t)
	retry := input
	retry.Visibility = VisibilityMaintainers
	retry.Metadata = json.RawMessage(`{"head_revision":"abc123","presentation":"new"}`)
	retried, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner),
		env.actor("code-change-retry"), env.scope, retry)
	if err != nil || retried.ID != first.ID || retried.RepresentationVersion != 1 || retried.Visibility != VisibilityRepository {
		t.Fatalf("exact retry = %+v, %v", retried, err)
	}
	afterIssue, afterRepo := env.referenceVersions(t)
	if beforeIssue != afterIssue || beforeRepo != afterRepo {
		t.Fatalf("exact retry bumped collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}

	urlDrift := input
	urlDrift.CanonicalURL += "/moved"
	assertCodeChangeConflict(t, env.upsertReference(t, "code-change-url-drift", urlDrift),
		CodeChangeConflictCanonicalURLDrift, first.ID)
	newHead := input
	newHead.Metadata = json.RawMessage(`{"head_revision":"def456"}`)
	assertCodeChangeConflict(t, env.upsertReference(t, "code-change-refresh-required", newHead),
		CodeChangeConflictRefreshRequired, first.ID)
	stale := int64(99)
	newHead.Refresh, newHead.ExpectedVersion = true, &stale
	assertCodeChangeConflict(t, env.upsertReference(t, "code-change-refresh-stale", newHead),
		CodeChangeConflictStaleReferenceVersion, first.ID)

	expected := first.RepresentationVersion
	newHead.ExpectedVersion = &expected
	newHead.CanonicalURL += "/revisions/def456"
	refreshed, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner),
		env.actor("code-change-refresh"), env.scope, newHead)
	if err != nil || refreshed.ID != first.ID || refreshed.RepresentationVersion != 2 ||
		!strings.Contains(string(refreshed.Metadata), "def456") {
		t.Fatalf("refresh = %+v, %v", refreshed, err)
	}
	refreshedRetry, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner),
		env.actor("code-change-refresh-retry"), env.scope, newHead)
	if err != nil || refreshedRetry.RepresentationVersion != 2 {
		t.Fatalf("refresh retry = %+v, %v", refreshedRetry, err)
	}
	staleAfterRefresh := input
	staleAfterRefresh.Metadata = json.RawMessage(`{"head_revision":"ghi789"}`)
	staleAfterRefresh.Refresh, staleAfterRefresh.ExpectedVersion = true, &expected
	assertCodeChangeConflict(t, env.upsertReference(t, "code-change-stale-after-refresh", staleAfterRefresh),
		CodeChangeConflictStaleReferenceVersion, first.ID)
	current, err := env.service.ListReferences(t.Context(), authz.Authenticated(env.owner), env.scope, env.issueID)
	if err != nil || len(current) != 1 || current[0].RepresentationVersion != 2 ||
		!strings.Contains(string(current[0].Metadata), "def456") {
		t.Fatalf("stale refresh changed current reference = %+v, %v", current, err)
	}
	assertCodeChangeConflict(t, env.upsertReference(t, "code-change-no-regression", input),
		CodeChangeConflictRefreshRequired, first.ID)

	var rows, audits int
	if err := env.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM external_references WHERE organization_id=$1 AND repository_id=$2 AND issue_id=$3),
		(SELECT count(*) FROM audit_events WHERE organization_id=$1 AND repository_id=$2
		 AND action='external_reference.upsert')`, env.scope.OrgID, env.scope.RepoID, env.issueID).Scan(&rows, &audits); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || audits != 2 {
		t.Fatalf("rows=%d audits=%d, want one establishment plus one refresh", rows, audits)
	}
}

func TestImplementCodeChangeConflictsWithDifferentAndAmbiguousActiveReferences(t *testing.T) {
	env := newBindingsEnvironment(t)
	env.markImplement(t)
	first, err := env.service.UpsertReference(t.Context(), authz.Authenticated(env.owner),
		env.actor("code-change-existing"), env.scope, env.codeChangeInput("42", "abc123"))
	if err != nil {
		t.Fatal(err)
	}
	assertCodeChangeConflict(t, env.upsertReference(t, "code-change-different", env.codeChangeInput("43", "abc123")),
		CodeChangeConflictDifferentActiveChange, first.ID)

	secondID := uuid.New()
	if _, err := env.pool.Exec(t.Context(), `INSERT INTO external_references
		(id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		 external_repository_id, external_id, canonical_url, lifecycle_state, visibility, metadata)
		VALUES ($1,$2,$3,$4,'aone-bridge','code_change','acme/widgets','41',
		 'https://code.example/acme/widgets/changes/41','active','maintainers','{"head_revision":"hidden"}')`,
		secondID, env.scope.OrgID, env.scope.RepoID, env.issueID); err != nil {
		t.Fatal(err)
	}
	beforeIssue, beforeRepo := env.referenceVersions(t)
	err = env.upsertReference(t, "code-change-ambiguous", env.codeChangeInput("44", "next"))
	var conflict *CodeChangeConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, adminservice.ErrConflict) ||
		conflict.Reason != CodeChangeConflictAmbiguousActiveReferences || len(conflict.References) != 2 {
		t.Fatalf("ambiguous conflict = %#v, %v", conflict, err)
	}
	if conflict.References[0].ID != secondID || conflict.References[1].ID != first.ID {
		t.Fatalf("ambiguous identities are not deterministic: %+v", conflict.References)
	}
	encoded, err := json.Marshal(conflict)
	if err != nil || strings.Contains(string(encoded), "canonical_url") || strings.Contains(string(encoded), "head_revision") ||
		strings.Contains(string(encoded), "hidden") {
		t.Fatalf("ambiguous diagnostics are not safe: %s, %v", encoded, err)
	}
	afterIssue, afterRepo := env.referenceVersions(t)
	if beforeIssue != afterIssue || beforeRepo != afterRepo {
		t.Fatalf("ambiguous conflict mutated collections issue %d/%d repo %d/%d", beforeIssue, afterIssue, beforeRepo, afterRepo)
	}
}

func TestImplementCodeChangeConflictRedactsMaintainerReferenceFromWriter(t *testing.T) {
	env := newBindingsEnvironment(t)
	env.markImplement(t)
	hiddenInput := env.codeChangeInput("hidden-change-901", "hidden-revision-901")
	hiddenInput.Visibility = VisibilityMaintainers
	hidden, err := env.upsertReferenceAs(t, env.owner, "hidden-reference", hiddenInput)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		input UpsertReferenceInput
	}{
		{name: "exact retry", input: hiddenInput},
		{name: "different change", input: env.codeChangeInput("writer-change-902", "writer-revision-902")},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := env.upsertReferenceAs(t, env.writer, "writer-hidden-conflict", test.input)
			if result.ID != uuid.Nil {
				t.Fatalf("writer received hidden reference result: %+v", result)
			}
			assertRedactedCodeChangeConflict(t, err, hidden, hiddenInput)
		})
	}

	writerItems, err := env.service.ListReferences(t.Context(), authz.Authenticated(env.writer), env.scope, env.issueID)
	if err != nil || len(writerItems) != 0 {
		t.Fatalf("writer references = %+v, %v; want hidden reference concealed", writerItems, err)
	}
	maintainerItems, err := env.service.ListReferences(t.Context(), authz.Authenticated(env.maintainer), env.scope, env.issueID)
	if err != nil || len(maintainerItems) != 1 || maintainerItems[0].ID != hidden.ID {
		t.Fatalf("maintainer references = %+v, %v", maintainerItems, err)
	}
	_, err = env.upsertReferenceAs(t, env.maintainer, "maintainer-hidden-conflict",
		env.codeChangeInput("maintainer-change-903", "maintainer-revision-903"))
	assertCodeChangeConflict(t, err, CodeChangeConflictDifferentActiveChange, hidden.ID)
}

func TestImplementCodeChangeMixedVisibilityConflictFailsClosedForWriter(t *testing.T) {
	env := newBindingsEnvironment(t)
	env.markImplement(t)
	visible, err := env.upsertReferenceAs(t, env.owner, "visible-reference",
		env.codeChangeInput("visible-change-911", "visible-revision-911"))
	if err != nil {
		t.Fatal(err)
	}
	hiddenID := uuid.New()
	if _, err := env.pool.Exec(t.Context(), `INSERT INTO external_references
		(id, organization_id, repository_id, issue_id, provider_key, relation_kind,
		 external_repository_id, external_id, canonical_url, lifecycle_state, visibility, metadata)
		VALUES ($1,$2,$3,$4,'hidden-provider-912','code_change','hidden/repository-912','hidden-change-912',
		 'https://hidden.example.test/changes/912','active','maintainers','{"head_revision":"hidden-revision-912"}')`,
		hiddenID, env.scope.OrgID, env.scope.RepoID, env.issueID); err != nil {
		t.Fatal(err)
	}

	_, err = env.upsertReferenceAs(t, env.writer, "writer-mixed-conflict",
		env.codeChangeInput("writer-change-913", "writer-revision-913"))
	var writerConflict *CodeChangeConflictError
	if !errors.As(err, &writerConflict) || !errors.Is(err, adminservice.ErrConflict) ||
		writerConflict.Reason != CodeChangeConflictHiddenActiveReferences || len(writerConflict.References) != 0 {
		t.Fatalf("writer mixed conflict = %#v, %v", writerConflict, err)
	}
	encoded, marshalErr := json.Marshal(writerConflict)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	for _, forbidden := range []string{visible.ID.String(), hiddenID.String(), "visible-change-911",
		"hidden-provider-912", "hidden/repository-912", "hidden-change-912", "hidden-revision-912",
		"https://hidden.example.test/changes/912", "representation_version", `"references":`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("writer mixed conflict exposed %q: %s", forbidden, encoded)
		}
	}

	_, err = env.upsertReferenceAs(t, env.maintainer, "maintainer-mixed-conflict",
		env.codeChangeInput("maintainer-change-914", "maintainer-revision-914"))
	var maintainerConflict *CodeChangeConflictError
	if !errors.As(err, &maintainerConflict) || maintainerConflict.Reason != CodeChangeConflictAmbiguousActiveReferences ||
		len(maintainerConflict.References) != 2 {
		t.Fatalf("maintainer mixed conflict = %#v, %v", maintainerConflict, err)
	}
	identities := map[uuid.UUID]bool{}
	for _, reference := range maintainerConflict.References {
		identities[reference.ID] = true
	}
	if !identities[visible.ID] || !identities[hiddenID] {
		t.Fatalf("maintainer did not receive complete diagnostics: %+v", maintainerConflict.References)
	}
}

func TestImplementRepositoryVisibleConflictRemainsRepairableForWriter(t *testing.T) {
	env := newBindingsEnvironment(t)
	env.markImplement(t)
	visible, err := env.upsertReferenceAs(t, env.owner, "visible-reference",
		env.codeChangeInput("visible-change-921", "visible-revision-921"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.upsertReferenceAs(t, env.writer, "writer-visible-conflict",
		env.codeChangeInput("writer-change-922", "writer-revision-922"))
	assertCodeChangeConflict(t, err, CodeChangeConflictDifferentActiveChange, visible.ID)
}

func TestCodeChangeSpecializationPreservesGenericReferenceUpsert(t *testing.T) {
	ordinary := newBindingsEnvironment(t)
	first := ordinary.codeChangeInput("42", "abc123")
	if _, err := ordinary.service.UpsertReference(t.Context(), authz.Authenticated(ordinary.owner),
		ordinary.actor("ordinary-first"), ordinary.scope, first); err != nil {
		t.Fatal(err)
	}
	second := ordinary.codeChangeInput("43", "def456")
	if _, err := ordinary.service.UpsertReference(t.Context(), authz.Authenticated(ordinary.owner),
		ordinary.actor("ordinary-second"), ordinary.scope, second); err != nil {
		t.Fatalf("ordinary issue rejected a second generic code change: %v", err)
	}

	implement := newBindingsEnvironment(t)
	implement.markImplement(t)
	build := implement.codeChangeInput("build-1", "abc123")
	build.RelationKind = "build"
	if _, err := implement.service.UpsertReference(t.Context(), authz.Authenticated(implement.owner),
		implement.actor("implement-build-first"), implement.scope, build); err != nil {
		t.Fatal(err)
	}
	build.ExternalID = "build-2"
	build.CanonicalURL = "https://code.example/acme/widgets/builds/2"
	if _, err := implement.service.UpsertReference(t.Context(), authz.Authenticated(implement.owner),
		implement.actor("implement-build-second"), implement.scope, build); err != nil {
		t.Fatalf("Implement Issue rejected a generic non-code_change reference: %v", err)
	}
}

func TestConcurrentImplementCodeChangeEstablishmentSerializesOnIssue(t *testing.T) {
	env := newBindingsEnvironment(t)
	env.markImplement(t)
	const requests = 8
	start := make(chan struct{})
	errorsCh := make(chan error, requests)
	results := make(chan Reference, requests)
	var wait sync.WaitGroup
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			input := env.codeChangeInput(fmt.Sprintf("change-%d", index), fmt.Sprintf("revision-%d", index))
			result, err := env.service.UpsertReference(context.Background(), authz.Authenticated(env.owner),
				env.actor(fmt.Sprintf("concurrent-code-change-%d", index)), env.scope, input)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	close(results)
	var successes, conflicts int
	for range results {
		successes++
	}
	for err := range errorsCh {
		if !errors.Is(err, adminservice.ErrConflict) {
			t.Fatalf("concurrent establishment error = %v", err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != requests-1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	var active, audits int
	if err := env.pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM external_references WHERE organization_id=$1 AND repository_id=$2
		 AND issue_id=$3 AND relation_kind='code_change' AND lifecycle_state='active'),
		(SELECT count(*) FROM audit_events WHERE organization_id=$1 AND repository_id=$2
		 AND action='external_reference.upsert')`, env.scope.OrgID, env.scope.RepoID, env.issueID).Scan(&active, &audits); err != nil {
		t.Fatal(err)
	}
	if active != 1 || audits != 1 {
		t.Fatalf("active=%d audits=%d, want 1/1", active, audits)
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
	maintainer serverauth.Principal
	writer     serverauth.Principal
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
	maintainerID := insertBindingsUser(t, pool, "maintainer")
	writerID := insertBindingsUser(t, pool, "writer")
	readerID := insertBindingsUser(t, pool, "reader")
	orgID := insertBindingsOrg(t, pool, "bindings-org")
	otherOrgID := insertBindingsOrg(t, pool, "bindings-other-org")
	repoID := insertBindingsRepo(t, pool, orgID, "repo")
	insertBindingsMembership(t, pool, orgID, ownerID, "owner")
	insertBindingsMembership(t, pool, orgID, maintainerID, "maintainer")
	insertBindingsMembership(t, pool, orgID, writerID, "member")
	insertBindingsMembership(t, pool, orgID, readerID, "reader")
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, 'write')`,
		orgID, repoID, writerID); err != nil {
		t.Fatal(err)
	}
	issueID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO issues
		(id, organization_id, repository_id, number, title) VALUES ($1, $2, $3, 1, 'issue')`, issueID, orgID, repoID); err != nil {
		t.Fatal(err)
	}
	return bindingsEnvironment{pool: pool, service: service, scope: models.RepoScope{OrgID: orgID, RepoID: repoID},
		otherOrgID: otherOrgID, issueID: issueID,
		owner: bindingsSession(t, pool, ownerID), maintainer: bindingsSession(t, pool, maintainerID),
		writer: bindingsSession(t, pool, writerID), reader: bindingsSession(t, pool, readerID)}
}

func (e bindingsEnvironment) actor(requestID string) adminservice.Actor {
	return adminservice.ActorFromPrincipal(e.owner, requestID)
}

func (e bindingsEnvironment) upsertReferenceAs(t *testing.T, principal serverauth.Principal, requestID string,
	input UpsertReferenceInput,
) (Reference, error) {
	t.Helper()
	return e.service.UpsertReference(t.Context(), authz.Authenticated(principal),
		adminservice.ActorFromPrincipal(principal, requestID), e.scope, input)
}

func (e bindingsEnvironment) markImplement(t *testing.T) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO issue_spec_artifacts
		(id, organization_id, repository_id, issue_id, change_key, artifact_type, content, metadata)
		VALUES ($1,$2,$3,$4,$5,'implement','valid implement marker','{"marker_version":1}')`,
		uuid.New(), e.scope.OrgID, e.scope.RepoID, e.issueID, "change-"+e.issueID.String()); err != nil {
		t.Fatal(err)
	}
}

func (e bindingsEnvironment) codeChangeInput(changeID, revision string) UpsertReferenceInput {
	return UpsertReferenceInput{IssueID: e.issueID, ProviderKey: "aone-bridge", RelationKind: "code_change",
		ExternalRepositoryID: "acme/widgets", ExternalID: changeID,
		CanonicalURL:   "https://code.example/acme/widgets/changes/" + changeID,
		LifecycleState: "active", Visibility: VisibilityRepository,
		Metadata: json.RawMessage(`{"head_revision":` + fmt.Sprintf("%q", revision) + `}`)}
}

func (e bindingsEnvironment) upsertReference(t *testing.T, requestID string, input UpsertReferenceInput) error {
	t.Helper()
	_, err := e.service.UpsertReference(t.Context(), authz.Authenticated(e.owner), e.actor(requestID), e.scope, input)
	return err
}

func assertCodeChangeConflict(t *testing.T, err error, reason CodeChangeConflictReason, referenceID uuid.UUID) {
	t.Helper()
	var conflict *CodeChangeConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, adminservice.ErrConflict) || conflict.Reason != reason ||
		len(conflict.References) != 1 || conflict.References[0].ID != referenceID {
		t.Fatalf("code-change conflict = %#v, %v; want reason=%s reference=%s", conflict, err, reason, referenceID)
	}
}

func assertRedactedCodeChangeConflict(t *testing.T, err error, hidden Reference, input UpsertReferenceInput) {
	t.Helper()
	var conflict *CodeChangeConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, adminservice.ErrConflict) ||
		conflict.Reason != CodeChangeConflictHiddenActiveReferences || len(conflict.References) != 0 {
		t.Fatalf("redacted code-change conflict = %#v, %v", conflict, err)
	}
	encoded, marshalErr := json.Marshal(conflict)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	diagnostics := string(encoded) + err.Error()
	for _, forbidden := range []string{hidden.ID.String(), input.ProviderKey, input.ExternalRepositoryID,
		input.ExternalID, input.CanonicalURL, "hidden-revision-901", "representation_version", "metadata", `"references":`} {
		if strings.Contains(diagnostics, forbidden) {
			t.Fatalf("redacted conflict exposed %q: %s", forbidden, diagnostics)
		}
	}
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
