package issues_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	commentapi "github.com/higress-group/issue-spec/internal/server/api/github/comments"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIssueCommentHTTPCompatibilityMarkerAndAuthorization(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPublic)
	if _, err := issueapi.NewService(store.New(environment.pool), environment.authorizer,
		artifacts.MarkerProjector{}, nil); err == nil {
		t.Fatal("nil mutation event hook was accepted")
	}
	mux := environment.mux(t)

	response := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues", "", "")
	if response.Code != http.StatusOK || response.Header().Get("ETag") == "" {
		t.Fatalf("anonymous list status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/issues", "", "invalid")
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"documentation_url"`) {
		t.Fatalf("invalid bearer response=%d %s", response.Code, response.Body.String())
	}

	rawIssue := "<!-- issue-spec:issue=proposal change=raw-change version=1 -->\r\n  # 标题  \r\n"
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
		jsonBody(map[string]any{"title": "Raw marker", "body": rawIssue}), "owner")
	if response.Code != http.StatusCreated {
		t.Fatalf("create issue status=%d body=%s", response.Code, response.Body.String())
	}
	var created codec.Issue
	decode(t, response, &created)
	if created.Body != rawIssue || created.User.Login != "owner" || !strings.HasPrefix(created.URL, "https://api.example.test/") ||
		strings.Contains(created.URL, "attacker") {
		t.Fatalf("created issue = %+v", created)
	}
	issueETag := response.Header().Get("ETag")
	conditional := httptest.NewRequest(http.MethodGet, "/repos/acme/widgets/issues/1", nil)
	conditional.Host = "attacker.invalid"
	conditional.Header.Set("If-None-Match", issueETag)
	conditionalResponse := httptest.NewRecorder()
	mux.ServeHTTP(conditionalResponse, conditional)
	if conditionalResponse.Code != http.StatusNotModified || conditionalResponse.Body.Len() != 0 {
		t.Fatalf("conditional issue status=%d body=%q", conditionalResponse.Code, conditionalResponse.Body.String())
	}
	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/issues?state=deleted", "", "")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"field":"state"`) {
		t.Fatalf("invalid state response=%d %s", response.Code, response.Body.String())
	}

	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues/1/comments", `{"body":""}`, "owner")
	if response.Code != http.StatusCreated {
		t.Fatalf("empty comment status=%d body=%s", response.Code, response.Body.String())
	}
	var comment codec.Comment
	decode(t, response, &comment)
	if comment.Body != "" || comment.IssueURL != "https://api.example.test/repos/acme/widgets/issues/1" {
		t.Fatalf("empty comment = %+v", comment)
	}
	var compatibilityID int64
	if err := environment.pool.QueryRow(t.Context(), `SELECT compatibility_id FROM comments WHERE id = $1`, stableUUIDFromNodeID(t, comment.NodeID)).Scan(&compatibilityID); err != nil {
		t.Fatal(err)
	}
	if compatibilityID != comment.ID {
		t.Fatalf("compatibility id=%d DTO id=%d", compatibilityID, comment.ID)
	}
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues/1/comments", `{"body":"\u0000"}`, "owner")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "NUL") {
		t.Fatalf("NUL comment response=%d %s", response.Code, response.Body.String())
	}

	typed := "<!-- issue-spec:type=SPEC id=SPEC-777 version=1 -->\nAgent: Worker\nType: SPEC\nID: SPEC-777\nStatus: confirmed\nScope: raw\nLinks:\n\n## Raw\n"
	response = request(t, mux, http.MethodPatch, fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", comment.ID),
		jsonBody(map[string]any{"body": typed}), "owner")
	if response.Code != http.StatusOK {
		t.Fatalf("typed update response=%d %s", response.Code, response.Body.String())
	}
	assertProjectionCounts(t, environment.pool, 1, 0)
	future := "<!-- issue-spec:type=SPEC id=SPEC-777 version=9 -->\nfuture raw\r\n"
	response = request(t, mux, http.MethodPatch, fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", comment.ID),
		jsonBody(map[string]any{"body": future}), "owner")
	if response.Code != http.StatusOK {
		t.Fatalf("future marker response=%d %s", response.Code, response.Body.String())
	}
	var updated codec.Comment
	decode(t, response, &updated)
	if updated.Body != future {
		t.Fatalf("future raw body = %q", updated.Body)
	}
	assertProjectionCounts(t, environment.pool, 0, 1)
	response = request(t, mux, http.MethodPatch, fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", comment.ID),
		jsonBody(map[string]any{"body": typed}), "owner")
	if response.Code != http.StatusOK {
		t.Fatalf("marker repair response=%d %s", response.Code, response.Body.String())
	}
	assertProjectionCounts(t, environment.pool, 1, 0)

	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues", jsonBody(map[string]any{"title": "second", "body": rawIssue}), "owner")
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	var activeArtifacts, duplicateAnomalies int
	if err := environment.pool.QueryRow(t.Context(), `SELECT count(*) FROM issue_spec_artifacts WHERE active`).Scan(&activeArtifacts); err != nil {
		t.Fatal(err)
	}
	if err := environment.pool.QueryRow(t.Context(), `SELECT count(*) FROM projection_anomalies
		WHERE anomaly_key = 'duplicate_issue_artifact' AND resolved_at IS NULL`).Scan(&duplicateAnomalies); err != nil {
		t.Fatal(err)
	}
	if activeArtifacts != 1 || duplicateAnomalies != 1 {
		t.Fatalf("duplicate issue projection active=%d anomalies=%d", activeArtifacts, duplicateAnomalies)
	}
	response = request(t, mux, http.MethodPatch, "/repos/acme/widgets/issues/2",
		jsonBody(map[string]any{"state": "closed"}), "owner")
	if response.Code != http.StatusOK {
		t.Fatalf("close second issue response=%d %s", response.Code, response.Body.String())
	}
	defaultList := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues?per_page=100", "", "")
	allList := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues?state=all&per_page=100", "", "")
	var defaultIssues, allIssues []codec.Issue
	decode(t, defaultList, &defaultIssues)
	decode(t, allList, &allIssues)
	if defaultList.Code != http.StatusOK || len(defaultIssues) != 1 || defaultIssues[0].State != "open" ||
		len(allIssues) != 2 || defaultList.Header().Get("ETag") == "" ||
		defaultList.Header().Get("ETag") == allList.Header().Get("ETag") {
		t.Fatalf("default state default=%d/%+v all=%d/%+v etags=%q/%q", defaultList.Code,
			defaultIssues, allList.Code, allIssues, defaultList.Header().Get("ETag"), allList.Header().Get("ETag"))
	}
	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/issues?state=all&per_page=1", "", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Link"), "https://api.example.test/") ||
		response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("pagination headers=%v", response.Header())
	}

	missing := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues/comments/9223372036854775806", "", "")
	other := newRepository(t, environment.pool, "other", "repo", models.VisibilityPublic)
	_ = other
	crossTenant := request(t, mux, http.MethodGet, fmt.Sprintf("/repos/other/repo/issues/comments/%d", comment.ID), "", "")
	if missing.Code != http.StatusNotFound || crossTenant.Code != http.StatusNotFound || missing.Body.String() != crossTenant.Body.String() {
		t.Fatalf("missing=%d/%q cross=%d/%q", missing.Code, missing.Body.String(), crossTenant.Code, crossTenant.Body.String())
	}

	reader := environment.addMember(t, "reader", "reader")
	otherReader := environment.addMember(t, "other-reader", "reader")
	insertLabel(t, environment.pool, environment.scope, "existing")
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
		jsonBody(map[string]any{"title": "reader labeled", "body": "raw", "labels": []string{"existing"}}), "reader")
	if response.Code != http.StatusForbidden {
		t.Fatalf("reader label assignment response=%d %s", response.Code, response.Body.String())
	}
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
		jsonBody(map[string]any{"title": "unknown label", "body": "raw", "labels": []string{"missing"}}), "owner")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"field":"labels"`) {
		t.Fatalf("unknown label response=%d %s", response.Code, response.Body.String())
	}
	otherRepoID := uuid.New()
	if _, err := environment.pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility, contribution_policy)
		VALUES ($1, $2, 'credential-cap', 'credential-cap', 'private', 'members')`,
		otherRepoID, environment.scope.OrgID); err != nil {
		t.Fatal(err)
	}
	restrictedPAT := insertRestrictedPAT(t, environment.pool, environment.owner, environment.scope.OrgID, otherRepoID)
	delegated := insertDelegated(t, environment.pool, environment.owner, environment.scope.OrgID, otherRepoID)
	environment.bearer.principals["restricted-pat"] = restrictedPAT
	environment.bearer.principals["delegated"] = delegated
	missingRepo := request(t, mux, http.MethodPost, "/repos/acme/missing/issues",
		jsonBody(map[string]any{"title": "missing", "body": "raw"}), "owner")
	for _, credential := range []string{"restricted-pat", "delegated"} {
		capped := request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
			jsonBody(map[string]any{"title": "capped", "body": "raw"}), credential)
		if capped.Code != http.StatusNotFound || capped.Body.String() != missingRepo.Body.String() {
			t.Fatalf("%s cap response=%d/%q missing=%d/%q", credential, capped.Code,
				capped.Body.String(), missingRepo.Code, missingRepo.Body.String())
		}
	}
	beforeDisabled := countRows(t, environment.pool, "issues")
	if _, err := environment.pool.Exec(t.Context(), `UPDATE repos SET contribution_policy = 'disabled'
		WHERE id = $1`, environment.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	disabled := request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
		jsonBody(map[string]any{"title": "disabled", "body": "raw", "labels": []string{"existing"}}), "owner")
	if disabled.Code != http.StatusForbidden || countRows(t, environment.pool, "issues") != beforeDisabled {
		t.Fatalf("disabled contribution status=%d rows=%d/%d body=%s", disabled.Code,
			beforeDisabled, countRows(t, environment.pool, "issues"), disabled.Body.String())
	}
	if _, err := environment.pool.Exec(t.Context(), `UPDATE repos SET contribution_policy = 'members'
		WHERE id = $1`, environment.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	_, authorComment, err := environment.service.CreateComment(t.Context(), "acme", "widgets", 1, authz.Authenticated(reader), "author body")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = environment.service.UpdateComment(t.Context(), "acme", "widgets",
		codec.StableNumericID(authorComment.Comment.ID.String()), authz.Authenticated(reader), "self edit")
	if err != nil {
		t.Fatalf("author self edit: %v", err)
	}
	_, _, err = environment.service.UpdateComment(t.Context(), "acme", "widgets",
		codec.StableNumericID(authorComment.Comment.ID.String()), authz.Authenticated(otherReader), "other edit")
	var denied *issueapi.DecisionError
	if !errors.As(err, &denied) || denied.Decision.Visible == false {
		t.Fatalf("other reader edit error = %v", err)
	}

	before := countRows(t, environment.pool, "issues")
	environment.hook.fail.Store(true)
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues", jsonBody(map[string]any{"title": "rollback", "body": "raw"}), "owner")
	environment.hook.fail.Store(false)
	if response.Code != http.StatusInternalServerError || countRows(t, environment.pool, "issues") != before {
		t.Fatalf("hook rollback status=%d before=%d after=%d", response.Code, before, countRows(t, environment.pool, "issues"))
	}
}

func TestConcurrentIssueCommentVersionsCASAndProjectionRollback(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	subject := authz.Authenticated(environment.owner)
	const issueCount = 32
	numbers := make(chan int64, issueCount)
	errorsCh := make(chan error, issueCount)
	var wait sync.WaitGroup
	for index := 0; index < issueCount; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
				models.NewIssue{Title: fmt.Sprintf("issue-%02d", index), Body: "raw"})
			if err == nil {
				numbers <- issue.Issue.Number
			}
			errorsCh <- err
		}(index)
	}
	wait.Wait()
	close(numbers)
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	var got []int
	for number := range numbers {
		got = append(got, int(number))
	}
	sort.Ints(got)
	for index, number := range got {
		if number != index+1 {
			t.Fatalf("issue numbers = %v", got)
		}
	}

	const commentCount = 32
	commentErrors := make(chan error, commentCount)
	for index := 0; index < commentCount; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, err := environment.service.CreateComment(t.Context(), "acme", "widgets", 1, subject, fmt.Sprintf("comment-%02d", index))
			commentErrors <- err
		}(index)
	}
	wait.Wait()
	close(commentErrors)
	for err := range commentErrors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var comments, issueCommentsVersion, repoCommentsVersion int
	if err := environment.pool.QueryRow(t.Context(), `SELECT count(*) FROM comments`).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := environment.pool.QueryRow(t.Context(), `SELECT comments_collection_version FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = 1`, environment.scope.OrgID, environment.scope.RepoID).Scan(&issueCommentsVersion); err != nil {
		t.Fatal(err)
	}
	if err := environment.pool.QueryRow(t.Context(), `SELECT comments_collection_version FROM repos WHERE id = $1`, environment.scope.RepoID).Scan(&repoCommentsVersion); err != nil {
		t.Fatal(err)
	}
	if comments != commentCount || issueCommentsVersion != commentCount+1 || repoCommentsVersion != commentCount+1 {
		t.Fatalf("comments=%d issueVersion=%d repoVersion=%d", comments, issueCommentsVersion, repoCommentsVersion)
	}

	repository := store.New(environment.pool).ScopedRepo(environment.scope)
	issue, err := repository.IssueByNumber(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	success := atomic.Int32{}
	casErrors := make(chan error, 32)
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := repository.UpdateIssueCAS(t.Context(), 1, issue.RepresentationVersion,
				models.IssueUpdate{Title: fmt.Sprintf("cas-%d", index), Body: issue.Body, State: issue.State})
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, store.ErrVersionConflict) {
				casErrors <- err
			}
		}(index)
	}
	wait.Wait()
	close(casErrors)
	for err := range casErrors {
		t.Fatal(err)
	}
	if success.Load() != 1 {
		t.Fatalf("CAS successes = %d", success.Load())
	}

	failing, err := issueapi.NewService(store.New(environment.pool), environment.authorizer, failingProjector{}, environment.hook)
	if err != nil {
		t.Fatal(err)
	}
	before := countRows(t, environment.pool, "issues")
	if _, _, err := failing.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "projector rollback", Body: "raw"}); err == nil {
		t.Fatal("projector failure unexpectedly committed")
	}
	if after := countRows(t, environment.pool, "issues"); after != before {
		t.Fatalf("projector failure rows before=%d after=%d", before, after)
	}

	blocking := newBlockingHook()
	linearized, err := issueapi.NewService(store.New(environment.pool), environment.authorizer, artifacts.MarkerProjector{}, blocking)
	if err != nil {
		t.Fatal(err)
	}
	mutationResult := make(chan error, 1)
	go func() {
		_, _, err := linearized.CreateIssue(context.Background(), "acme", "widgets", subject,
			models.NewIssue{Title: "linearized before revoke", Body: "raw"})
		mutationResult <- err
	}()
	<-blocking.reached
	revokeResult := make(chan error, 1)
	go func() {
		_, err := environment.pool.Exec(context.Background(), `UPDATE sessions SET revoked_at = clock_timestamp()
			WHERE id = $1`, environment.owner.CredentialID)
		revokeResult <- err
	}()
	select {
	case err := <-revokeResult:
		t.Fatalf("revocation bypassed mutation credential lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(blocking.release)
	if err := <-mutationResult; err != nil {
		t.Fatal(err)
	}
	if err := <-revokeResult; err != nil {
		t.Fatal(err)
	}
	rowsAfterLinearized := countRows(t, environment.pool, "issues")
	if _, _, err := linearized.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "must fail after revoke", Body: "raw"}); err == nil {
		t.Fatal("revoked credential mutation unexpectedly succeeded")
	}
	if after := countRows(t, environment.pool, "issues"); after != rowsAfterLinearized {
		t.Fatalf("revoked mutation rows before=%d after=%d", rowsAfterLinearized, after)
	}
}

func TestConcurrentDuplicateProjectionAndRepeatableReadSnapshot(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	repository := store.New(environment.pool).ScopedRepo(environment.scope)
	raw := "<!-- issue-spec:issue=proposal change=duplicate version=1 -->\nraw"
	first, err := repository.CreateIssue(t.Context(), models.NewIssue{ID: uuid.New(), AuthorID: &environment.owner.User.ID, Title: "first", Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateIssue(t.Context(), models.NewIssue{ID: uuid.New(), AuthorID: &environment.owner.User.ID, Title: "second", Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	projector := artifacts.MarkerProjector{}
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- projector.ProjectIssue(context.Background(), repository, first) }()
	go func() { errorsCh <- projector.ProjectIssue(context.Background(), repository, second) }()
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	var rawRows, active, anomalies int
	if err := environment.pool.QueryRow(t.Context(), `SELECT count(*) FROM issues WHERE body = $1`, raw).Scan(&rawRows); err != nil {
		t.Fatal(err)
	}
	if err := environment.pool.QueryRow(t.Context(), `SELECT count(*) FROM issue_spec_artifacts
		WHERE change_key = 'duplicate' AND artifact_type = 'proposal' AND active`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := environment.pool.QueryRow(t.Context(), `SELECT count(*) FROM projection_anomalies
		WHERE anomaly_key = 'duplicate_issue_artifact' AND resolved_at IS NULL`).Scan(&anomalies); err != nil {
		t.Fatal(err)
	}
	if rawRows != 2 || active != 1 || anomalies != 1 {
		t.Fatalf("raw=%d active=%d anomalies=%d", rawRows, active, anomalies)
	}

	database := store.New(environment.pool)
	started := make(chan struct{})
	updated := make(chan struct{})
	var before, after models.IssuePage
	readErr := make(chan error, 1)
	go func() {
		readErr <- database.WithTx(context.Background(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
			var err error
			before, err = tx.ScopedRepo(environment.scope).ListIssues(context.Background(), models.IssueListOptions{Page: 1, PerPage: 100})
			if err != nil {
				return err
			}
			close(started)
			<-updated
			after, err = tx.ScopedRepo(environment.scope).ListIssues(context.Background(), models.IssueListOptions{Page: 1, PerPage: 100})
			return err
		})
	}()
	<-started
	if _, err := environment.pool.Exec(t.Context(), `UPDATE issues SET title = 'new title',
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.pool.Exec(t.Context(), `UPDATE repos SET issues_collection_version = issues_collection_version + 1
		WHERE id = $1`, environment.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	close(updated)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	if before.CollectionVersion != after.CollectionVersion || before.Items[0].Issue.Title != after.Items[0].Issue.Title {
		t.Fatalf("repeatable snapshot changed: before=%+v after=%+v", before, after)
	}
}

type environment struct {
	pool       *pgxpool.Pool
	scope      models.RepoScope
	owner      serverauth.Principal
	authorizer *authz.Service
	service    *issueapi.Service
	hook       *outboxHook
	origins    publicurl.Origins
	bearer     *testBearer
}

func newEnvironment(t *testing.T, visibility models.Visibility) *environment {
	pool := migratedPool(t)
	userID := insertUser(t, pool, "owner")
	scope := newRepository(t, pool, "acme", "widgets", visibility)
	insertMembership(t, pool, scope.OrgID, userID, "owner")
	sessionID := insertSession(t, pool, userID)
	owner := serverauth.Principal{User: serverauth.User{ID: userID, Login: "owner", Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: sessionID}
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	hook := &outboxHook{}
	service, err := issueapi.NewService(store.New(pool), authorizer, artifacts.MarkerProjector{}, hook)
	if err != nil {
		t.Fatal(err)
	}
	origins, err := publicurl.New("https://api.example.test", "https://web.example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return &environment{pool: pool, scope: scope, owner: owner, authorizer: authorizer,
		service: service, hook: hook, origins: origins, bearer: &testBearer{principals: map[string]serverauth.Principal{"owner": owner}}}
}

func (e *environment) addMember(t *testing.T, login, role string) serverauth.Principal {
	userID := insertUser(t, e.pool, login)
	insertMembership(t, e.pool, e.scope.OrgID, userID, role)
	principal := serverauth.Principal{User: serverauth.User{ID: userID, Login: login, Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: insertSession(t, e.pool, userID)}
	e.bearer.principals[login] = principal
	return principal
}

func (e *environment) mux(t *testing.T) *http.ServeMux {
	t.Helper()
	authentication := serverauth.Middleware{Bearer: e.bearer}
	issueRoutes, err := issueapi.NewRouteSet(issueapi.Dependencies{Service: e.service,
		Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication})
	if err != nil {
		t.Fatal(err)
	}
	commentRoutes, err := commentapi.NewRouteSet(commentapi.Dependencies{Service: e.service,
		Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.SelfHostedPolicy(), issueRoutes, commentRoutes)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

type testBearer struct {
	principals map[string]serverauth.Principal
}

func (b *testBearer) AuthenticateBearer(_ context.Context, token string) (serverauth.Principal, error) {
	principal, ok := b.principals[token]
	if !ok {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	return principal, nil
}

type outboxHook struct{ fail atomic.Bool }

func (h *outboxHook) Emit(ctx context.Context, repository store.RepoStore, event issueapi.MutationEvent) error {
	if h.fail.Load() {
		return errors.New("injected hook failure")
	}
	payload, _ := json.Marshal(map[string]any{"event": event.Type, "raw_body": event.RawBody,
		"body_hash": fmt.Sprintf("%x", event.BodyHash), "version": event.RepresentationVersion})
	_, err := repository.EnqueueEvent(ctx, models.NewOutboxEvent{AggregateType: "issue",
		AggregateID: event.Issue.ID, EventType: event.Type, EventKey: event.Key, Payload: payload})
	return err
}

type blockingHook struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingHook() *blockingHook {
	return &blockingHook{reached: make(chan struct{}), release: make(chan struct{})}
}

func (h *blockingHook) Emit(context.Context, store.RepoStore, issueapi.MutationEvent) error {
	h.once.Do(func() { close(h.reached) })
	<-h.release
	return nil
}

type failingProjector struct{}

func (failingProjector) ProjectIssue(context.Context, store.RepoStore, models.Issue) error {
	return errors.New("injected projector failure")
}
func (failingProjector) ProjectComment(context.Context, store.RepoStore, models.CommentSnapshot) error {
	return errors.New("injected projector failure")
}

func request(t *testing.T, handler http.Handler, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Host = "attacker.invalid"
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func jsonBody(value any) string { encoded, _ := json.Marshal(value); return string(encoded) }

func decode(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func stableUUIDFromNodeID(t *testing.T, nodeID string) uuid.UUID {
	t.Helper()
	decoded, err := base64.RawStdEncoding.DecodeString(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	_, value, ok := strings.Cut(string(decoded), ":")
	if !ok {
		t.Fatalf("invalid node id %q", nodeID)
	}
	id, err := uuid.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func assertProjectionCounts(t *testing.T, pool *pgxpool.Pool, active, unresolved int) {
	t.Helper()
	var gotActive, gotUnresolved int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM issue_spec_typed_comments`).Scan(&gotActive); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM projection_anomalies WHERE resolved_at IS NULL`).Scan(&gotUnresolved); err != nil {
		t.Fatal(err)
	}
	if gotActive != active || gotUnresolved != unresolved {
		t.Fatalf("projection counts active=%d unresolved=%d", gotActive, gotUnresolved)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "issue_api_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 64
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

func insertUser(t *testing.T, pool *pgxpool.Pool, login string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return id
}

func newRepository(t *testing.T, pool *pgxpool.Pool, owner, name string, visibility models.Visibility) models.RepoScope {
	t.Helper()
	orgID, repoID := uuid.New(), uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission)
		VALUES ($1, $2, $2, 'read')`, orgID, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility, contribution_policy)
		VALUES ($1, $2, $3, $3, $4, 'members')`, repoID, orgID, name, visibility); err != nil {
		t.Fatal(err)
	}
	return models.RepoScope{OrgID: orgID, RepoID: repoID}
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`,
		orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func insertSession(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		id, userID, "session-"+id.String(), []byte(id.String()), []byte("csrf-"+id.String())); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertLabel(t *testing.T, pool *pgxpool.Pool, scope models.RepoScope, name string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `INSERT INTO labels
		(id, organization_id, repository_id, name, color) VALUES ($1, $2, $3, $4, 'abcdef')`,
		uuid.New(), scope.OrgID, scope.RepoID, name); err != nil {
		t.Fatal(err)
	}
}

func insertRestrictedPAT(t *testing.T, pool *pgxpool.Pool, owner serverauth.Principal, orgID, repoID uuid.UUID) serverauth.Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO personal_access_tokens
		(id, user_id, name, token_prefix, token_hash, expires_at)
		VALUES ($1, $2, 'restricted', $3, $4, clock_timestamp() + interval '1 hour')`,
		id, owner.User.ID, "pat-"+id.String(), []byte(id.String())); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO pat_scopes
		(id, personal_access_token_id, scope) VALUES ($1, $2, 'issues:write')`, uuid.New(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO pat_repositories
		(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`, id, orgID, repoID); err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{User: owner.User, Kind: serverauth.CredentialPAT, CredentialID: id,
		Scopes: []string{"issues:write"}, RepoRestricted: true,
		RepositoryCaps: []serverauth.RepositoryCap{{OrgID: orgID, RepoID: repoID}}}
}

func insertDelegated(t *testing.T, pool *pgxpool.Pool, owner serverauth.Principal, orgID, repoID uuid.UUID) serverauth.Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO delegated_tokens
		(id, user_id, organization_id, repository_id, job_id, purpose, token_hash,
		 audience, subject, claims, expires_at)
		VALUES ($1, $2, $3, $4, 'job', 'writeback', $5, 'issue-spec-server',
		 'runner-child', '{"scopes":["issues:write"]}', clock_timestamp() + interval '1 hour')`,
		id, owner.User.ID, orgID, repoID, []byte(id.String())); err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{User: owner.User, Kind: serverauth.CredentialDelegated, CredentialID: id,
		Scopes: []string{"issues:write"}, RepoRestricted: true, OrgID: orgID, RepoID: repoID,
		RepositoryCaps: []serverauth.RepositoryCap{{OrgID: orgID, RepoID: repoID}}}
}
