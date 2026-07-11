package changes

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

func TestRepositoryProjectionLifecycleProgressAndAnomalies(t *testing.T) {
	env := newChangesEnvironment(t)
	proposal := env.addArtifact(t, env.scope, "alpha", StageProposal, "1", "issue-spec/proposal", "N/A", "N/A")
	design := env.addArtifact(t, env.scope, "alpha", StageDesign, "1", "issue-spec/design", proposalURL(proposal), "N/A")
	implement := env.addArtifact(t, env.scope, "alpha", StageImplement, "1", "issue-spec/design", "N/A", "N/A")
	_ = env.addArtifact(t, env.scope, "alpha", StageDesign, "1", "issue-spec/design", proposalURL(proposal), "N/A")
	_ = env.addArtifact(t, env.scope, "alpha", StageProposal, "2", "issue-spec/proposal", "N/A", "N/A")
	_ = env.addArtifact(t, env.scope, "orphan-implement", StageImplement, "1", "issue-spec/implement", "N/A", proposalURL(design))

	env.addTyped(t, env.scope, proposal.ID, "TASK", "TASK-001", "done", nil)
	env.addTyped(t, env.scope, design.ID, "TASK", "TASK-002", "in-progress", nil)
	env.addTyped(t, env.scope, design.ID, "TASK", "TASK-003", "superseded", nil)
	env.addTyped(t, env.scope, implement.ID, "PROCESS", "PROCESS-001", "blocked", nil)
	env.addTyped(t, env.scope, implement.ID, "PROCESS", "PROCESS-002", "confirmed", nil)
	env.addTyped(t, env.scope, proposal.ID, "QUESTION", "QUESTION-001", "blocked", nil)
	env.addTyped(t, env.scope, implement.ID, "VERIFY", "VERIFY-001", "done", []string{"N/A"})
	env.addOrphanTyped(t, env.scope, "TASK", "TASK-ORPHAN", "ready")

	page, err := env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Cards) != 2 || page.Counts.Total != 2 || page.Counts.Blocked != 1 || page.Counts.Implement != 2 {
		t.Fatalf("board cards/counts = %+v / %+v", page.Cards, page.Counts)
	}
	alpha := requireCard(t, page.Cards, "alpha")
	if alpha.CurrentStage != StageImplement || alpha.Lifecycle != LifecycleBlocked || alpha.Artifacts.Proposal == nil ||
		alpha.Artifacts.Design == nil || alpha.Artifacts.Implement == nil || alpha.Artifacts.Proposal.MarkerVersion != "1" {
		t.Fatalf("alpha projection = %+v", alpha)
	}
	if alpha.Tasks != (Progress{Total: 2, Completed: 1, InProgress: 1}) ||
		alpha.Processes != (Progress{Total: 2, Blocked: 1, Pending: 1}) {
		t.Fatalf("progress tasks=%+v processes=%+v", alpha.Tasks, alpha.Processes)
	}
	for _, code := range []string{AnomalyDuplicateArtifactType, AnomalyMarkerLabelMismatch,
		AnomalyMissingRequiredLinks, AnomalyUnsupportedMarkerVersion} {
		if !contains(alpha.Anomalies, code) {
			t.Fatalf("alpha anomalies %v missing %s", alpha.Anomalies, code)
		}
	}
	orphan := requireCard(t, page.Cards, "orphan-implement")
	if !contains(orphan.Anomalies, AnomalyImplementMissingPredecessor) {
		t.Fatalf("orphan anomalies = %v", orphan.Anomalies)
	}
	if diagnosticCount(page.Diagnostics, AnomalyOrphanTypedArtifact) != 1 {
		t.Fatalf("diagnostics = %+v", page.Diagnostics)
	}

	env.updateTyped(t, env.scope, "QUESTION-001", "confirmed", nil)
	env.closeIssues(t, env.scope, proposal.ID, design.ID, implement.ID)
	page, err = env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := requireCard(t, page.Cards, "alpha").Lifecycle; got != LifecycleClosed {
		t.Fatalf("placeholder PR incorrectly completed lifecycle: %s", got)
	}
	// A completed projection accepts the semantic PR evidence field without
	// imposing a GitHub-specific /pull/ URL shape on external code providers.
	env.updateTyped(t, env.scope, "VERIFY-001", "done", []string{"https://code.example/acme/widgets/-/merge_requests/42"})
	page, err = env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := requireCard(t, page.Cards, "alpha").Lifecycle; got != LifecycleCompleted {
		t.Fatalf("verified closed lifecycle = %s", got)
	}

	filtered, err := env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope,
		ListOptions{Stage: StageImplement, Lifecycle: LifecycleCompleted, Anomaly: AnomalyMissingRequiredLinks, Page: 1, PerPage: 1})
	if err != nil || filtered.Total != 1 || len(filtered.Cards) != 1 || filtered.Cards[0].ChangeKey != "alpha" {
		t.Fatalf("filtered = %+v, %v", filtered, err)
	}
	emptyFiltered, err := env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope,
		ListOptions{Stage: StageProposal, Page: 1, PerPage: 12})
	if err != nil || emptyFiltered.Total != 0 || emptyFiltered.Cards == nil || len(emptyFiltered.Cards) != 0 {
		t.Fatalf("empty filtered board = %+v, %v", emptyFiltered, err)
	}
	encoded, err := json.Marshal(emptyFiltered)
	if err != nil || !strings.Contains(string(encoded), `"cards":[]`) {
		t.Fatalf("empty filtered board JSON = %s, %v", encoded, err)
	}
}

func TestChangeDetailIsNotLimitedToFirstHundredAndTenantKeysStayIndependent(t *testing.T) {
	env := newChangesEnvironment(t)
	for index := 0; index < 105; index++ {
		env.addArtifact(t, env.scope, fmt.Sprintf("change-%03d", index), StageProposal, "1", "issue-spec/proposal", "N/A", "N/A")
	}
	card, validator, _, err := env.service.Change(t.Context(), authz.Authenticated(env.principal), env.scope, " CHANGE-104 ")
	if err != nil || card.ChangeKey != "change-104" || validator == "" {
		t.Fatalf("detail = %+v validator=%q err=%v", card, validator, err)
	}

	otherOrg := env.insertOrg(t, "other", "none")
	otherRepo := env.insertRepo(t, otherOrg, "widgets", "private")
	env.addMembership(t, otherOrg, env.principal.User.ID, "owner")
	otherScope := models.RepoScope{OrgID: otherOrg, RepoID: otherRepo}
	env.addArtifact(t, otherScope, "change-104", StageImplement, "1", "issue-spec/implement", "N/A", "https://code.example/design")
	other, err := env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), otherScope, ListOptions{})
	if err != nil || len(other.Cards) != 1 || other.Cards[0].CurrentStage != StageImplement || other.Cards[0].Repository.ID != otherRepo {
		t.Fatalf("cross-tenant board = %+v, %v", other, err)
	}
}

func TestOrganizationFilteringAndAuthorizationRecheckPreventLeaks(t *testing.T) {
	env := newChangesEnvironment(t)
	visible := env.addArtifact(t, env.scope, "visible", StageProposal, "1", "issue-spec/proposal", "N/A", "N/A")
	hiddenRepo := env.insertRepo(t, env.scope.OrgID, "hidden", "private")
	hiddenScope := models.RepoScope{OrgID: env.scope.OrgID, RepoID: hiddenRepo}
	env.addArtifact(t, hiddenScope, "hidden", StageProposal, "2", "issue-spec/proposal", "N/A", "N/A")
	if _, err := env.pool.Exec(t.Context(), `UPDATE repos SET archived_at = clock_timestamp() WHERE organization_id = $1 AND id = $2`, env.scope.OrgID, hiddenRepo); err != nil {
		t.Fatal(err)
	}
	page, err := env.service.OrganizationBoard(t.Context(), authz.Authenticated(env.principal), models.OrgScope{OrgID: env.scope.OrgID}, ListOptions{})
	if err != nil || len(page.Cards) != 1 || page.Counts.Total != 1 || page.Cards[0].ChangeKey != "visible" ||
		strings.Contains(mustJSON(t, page), hiddenRepo.String()) || diagnosticCount(page.Diagnostics, AnomalyUnsupportedMarkerVersion) != 0 ||
		page.Cards[0].Artifacts.Proposal.ID != visible.ID {
		t.Fatalf("organization board leaked hidden repository: %+v err=%v", page, err)
	}

	var once sync.Once
	env.service.beforeSnapshot = func() {
		once.Do(func() {
			if _, err := env.pool.Exec(t.Context(), `UPDATE org_memberships SET archived_at = clock_timestamp()
				WHERE organization_id = $1 AND user_id = $2`, env.scope.OrgID, env.principal.User.ID); err != nil {
				t.Error(err)
			}
		})
	}
	page, err = env.service.OrganizationBoard(t.Context(), authz.Authenticated(env.principal), models.OrgScope{OrgID: env.scope.OrgID}, ListOptions{})
	if err != nil || len(page.Cards) != 0 || page.Counts.Total != 0 || len(page.Diagnostics) != 0 {
		t.Fatalf("revoked organization board = %+v err=%v", page, err)
	}

	env2 := newChangesEnvironment(t)
	env2.addArtifact(t, env2.scope, "private", StageProposal, "1", "issue-spec/proposal", "N/A", "N/A")
	env2.service.beforeSnapshot = func() {
		env2.service.beforeSnapshot = nil
		if _, err := env2.pool.Exec(t.Context(), `UPDATE org_memberships SET archived_at = clock_timestamp()
			WHERE organization_id = $1 AND user_id = $2`, env2.scope.OrgID, env2.principal.User.ID); err != nil {
			t.Error(err)
		}
	}
	_, err = env2.service.RepositoryBoard(t.Context(), authz.Authenticated(env2.principal), env2.scope, ListOptions{})
	if !errors.Is(err, adminservice.ErrNotFound) {
		t.Fatalf("revoked repository error = %v", err)
	}
}

func TestRepeatableReadKeepsTypedProgressAndValidatorConsistent(t *testing.T) {
	env := newChangesEnvironment(t)
	proposal := env.addArtifact(t, env.scope, "snapshot", StageProposal, "1", "issue-spec/proposal", "N/A", "N/A")
	env.addTyped(t, env.scope, proposal.ID, "TASK", "TASK-001", "confirmed", nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	env.service.afterArtifactLoad = func() { once.Do(func() { close(entered); <-release }) }
	type result struct {
		page BoardPage
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		page, err := env.service.RepositoryBoard(context.Background(), authz.Authenticated(env.principal), env.scope, ListOptions{})
		resultCh <- result{page: page, err: err}
	}()
	<-entered
	env.updateTyped(t, env.scope, "TASK-001", "done", nil)
	close(release)
	first := <-resultCh
	if first.err != nil || len(first.page.Cards) != 1 || first.page.Cards[0].Tasks.Pending != 1 || first.page.Cards[0].Tasks.Completed != 0 {
		t.Fatalf("snapshot result = %+v err=%v", first.page, first.err)
	}
	env.service.afterArtifactLoad = nil
	if _, err := env.pool.Exec(t.Context(), `UPDATE repos SET artifacts_collection_version = artifacts_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND id = $2`, env.scope.OrgID, env.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	second, err := env.service.RepositoryBoard(t.Context(), authz.Authenticated(env.principal), env.scope, ListOptions{})
	if err != nil || second.Cards[0].Tasks.Completed != 1 || second.Validator == first.page.Validator {
		t.Fatalf("next snapshot = %+v err=%v first validator=%s", second, err, first.page.Validator)
	}
}

type changesEnvironment struct {
	pool      *pgxpool.Pool
	service   *Service
	scope     models.RepoScope
	principal serverauth.Principal
}

func newChangesEnvironment(t *testing.T) *changesEnvironment {
	t.Helper()
	pool := changesPool(t)
	env := &changesEnvironment{pool: pool}
	userID := env.insertUser(t, "owner")
	orgID := env.insertOrg(t, "acme", "none")
	repoID := env.insertRepo(t, orgID, "widgets", "private")
	env.addMembership(t, orgID, userID, "owner")
	sessionID := env.insertSession(t, userID)
	authorization, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(pool, authorization)
	if err != nil {
		t.Fatal(err)
	}
	env.service, env.scope = service, models.RepoScope{OrgID: orgID, RepoID: repoID}
	env.principal = serverauth.Principal{User: serverauth.User{ID: userID, Login: "owner", Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: sessionID}
	return env
}

func (e *changesEnvironment) addArtifact(t *testing.T, scope models.RepoScope, change string, kind Stage, version, label, proposal, design string) models.Issue {
	t.Helper()
	body := fmt.Sprintf("<!-- issue-spec:issue=%s change=%s version=%s -->\n# %s\n- Proposal Issue: %s\n- Design Issue: %s\n",
		kind, change, version, change, proposal, design)
	repository := store.New(e.pool).ScopedRepo(scope)
	issue, err := repository.CreateIssue(t.Context(), models.NewIssue{ID: uuid.New(), Title: change, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if version == "1" && knownStage(kind) {
		err = repository.ApplyIssueProjection(t.Context(), store.IssueProjectionInput{IssueID: issue.ID,
			ChangeKey: change, ArtifactType: string(kind), Content: body, Metadata: json.RawMessage(`{"marker_version":1}`)})
		if errors.Is(err, store.ErrProjectionConflict) {
			err = repository.RecordProjectionAnomaly(t.Context(), store.ProjectionAnomalyInput{SourceType: "issue", SourceID: issue.ID,
				Key: "duplicate_issue_artifact", Details: json.RawMessage(`{"reason":"duplicate"}`)})
		}
	} else {
		err = repository.RecordProjectionAnomaly(t.Context(), store.ProjectionAnomalyInput{SourceType: "issue", SourceID: issue.ID,
			Key: "unsupported_issue_marker", Details: json.RawMessage(`{"reason":"unsupported"}`)})
	}
	if err != nil {
		t.Fatal(err)
	}
	if label != "" {
		e.assignLabel(t, scope, issue.ID, label)
	}
	if _, err := repository.IncrementCollectionVersions(t.Context(), store.RepoCollectionArtifacts); err != nil {
		t.Fatal(err)
	}
	return issue
}

func (e *changesEnvironment) assignLabel(t *testing.T, scope models.RepoScope, issueID uuid.UUID, name string) {
	t.Helper()
	var labelID uuid.UUID
	err := e.pool.QueryRow(t.Context(), `INSERT INTO labels (id, organization_id, repository_id, name, color)
		VALUES ($1, $2, $3, $4, '0f6f6f') ON CONFLICT (organization_id, repository_id, name_key)
		DO UPDATE SET name = EXCLUDED.name RETURNING id`, uuid.New(), scope.OrgID, scope.RepoID, name).Scan(&labelID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO issue_labels (organization_id, repository_id, issue_id, label_id)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, scope.OrgID, scope.RepoID, issueID, labelID); err != nil {
		t.Fatal(err)
	}
}

func (e *changesEnvironment) addTyped(t *testing.T, scope models.RepoScope, issueID uuid.UUID, typ, key, status string, prLinks []string) {
	t.Helper()
	var artifactID uuid.UUID
	if err := e.pool.QueryRow(t.Context(), `SELECT id FROM issue_spec_artifacts WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active`,
		scope.OrgID, scope.RepoID, issueID).Scan(&artifactID); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"status": status, "links": map[string]any{"PR": prLinks}})
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO issue_spec_typed_comments
		(id, organization_id, repository_id, issue_id, artifact_id, comment_type, comment_key, body, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'safe projected body', $8::jsonb)`, uuid.New(), scope.OrgID, scope.RepoID,
		issueID, artifactID, typ, key, string(metadata)); err != nil {
		t.Fatal(err)
	}
}

func (e *changesEnvironment) addOrphanTyped(t *testing.T, scope models.RepoScope, typ, key, status string) {
	t.Helper()
	repository := store.New(e.pool).ScopedRepo(scope)
	issue, err := repository.CreateIssue(t.Context(), models.NewIssue{ID: uuid.New(), Title: "ordinary", Body: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	commentID := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO comments (id, organization_id, repository_id, issue_id, body)
		VALUES ($1, $2, $3, $4, 'ordinary comment')`, commentID, scope.OrgID, scope.RepoID, issue.ID); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"status": status})
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO issue_spec_typed_comments
		(id, organization_id, repository_id, issue_id, comment_id, comment_type, comment_key, body, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'safe orphan body', $8::jsonb)`, uuid.New(), scope.OrgID, scope.RepoID,
		issue.ID, commentID, typ, key, string(metadata)); err != nil {
		t.Fatal(err)
	}
}

func (e *changesEnvironment) updateTyped(t *testing.T, scope models.RepoScope, key, status string, prLinks []string) {
	t.Helper()
	metadata, _ := json.Marshal(map[string]any{"status": status, "links": map[string]any{"PR": prLinks}})
	if _, err := e.pool.Exec(t.Context(), `UPDATE issue_spec_typed_comments SET metadata = $4::jsonb,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND comment_key = $3`, scope.OrgID, scope.RepoID, key, string(metadata)); err != nil {
		t.Fatal(err)
	}
}

func (e *changesEnvironment) closeIssues(t *testing.T, scope models.RepoScope, ids ...uuid.UUID) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `UPDATE issues SET state = 'closed', closed_at = clock_timestamp(),
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND id = ANY($3::uuid[])`, scope.OrgID, scope.RepoID, ids); err != nil {
		t.Fatal(err)
	}
}

func (e *changesEnvironment) insertUser(t *testing.T, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login+id.String()); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *changesEnvironment) insertOrg(t *testing.T, name, base string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission) VALUES ($1, $2, $2, $3)`, id, name+id.String(), base); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *changesEnvironment) insertRepo(t *testing.T, orgID uuid.UUID, name, visibility string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO repos (id, organization_id, name, display_name, visibility)
		VALUES ($1, $2, $3, $3, $4)`, id, orgID, name+id.String(), visibility); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *changesEnvironment) addMembership(t *testing.T, orgID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at)
		VALUES ($1, $2, $3, 'active', clock_timestamp())`, orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func (e *changesEnvironment) insertSession(t *testing.T, userID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		id, userID, "session-"+id.String(), []byte(id.String()), []byte("csrf")); err != nil {
		t.Fatal(err)
	}
	return id
}

func changesPool(t *testing.T) *pgxpool.Pool {
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
	schema := "changes_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
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

func requireCard(t *testing.T, cards []ChangeCard, key string) ChangeCard {
	t.Helper()
	for _, card := range cards {
		if card.ChangeKey == key {
			return card
		}
	}
	t.Fatalf("card %q not found in %+v", key, cards)
	return ChangeCard{}
}

func diagnosticCount(values []DiagnosticCount, code string) int {
	for _, value := range values {
		if value.Code == code {
			return value.Count
		}
	}
	return 0
}

func proposalURL(issue models.Issue) string {
	return "https://code.example/issues/" + fmt.Sprint(issue.Number)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
