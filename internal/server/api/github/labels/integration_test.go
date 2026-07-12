package labels_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	commentapi "github.com/higress-group/issue-spec/internal/server/api/github/comments"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	issueapi "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	labelapi "github.com/higress-group/issue-spec/internal/server/api/github/labels"
	permissionapi "github.com/higress-group/issue-spec/internal/server/api/github/permissions"
	reactionapi "github.com/higress-group/issue-spec/internal/server/api/github/reactions"
	subscriptionapi "github.com/higress-group/issue-spec/internal/server/api/github/subscription"
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

func TestProtocolFeatureHTTPAndVersionInvalidation(t *testing.T) {
	e := newEnvironment(t)
	mux := e.mux(t)

	response := request(t, mux, http.MethodPost, "/repos/acme/widgets/labels", `{"name":"Bug","color":"ff0000"}`, "owner", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create label = %d %s", response.Code, response.Body.String())
	}
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/labels", `{"name":"bug","color":"00ff00"}`, "owner", nil)
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "already_exists") {
		t.Fatalf("duplicate label = %d %s", response.Code, response.Body.String())
	}
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/labels", `{"name":"Priority","color":"0000ff"}`, "owner", nil)
	if response.Code != http.StatusCreated {
		t.Fatal(response.Body.String())
	}
	if got := request(t, mux, http.MethodPost, "/repos/acme/widgets/labels", `{"name":"reader-label","color":"123456"}`, "reader", nil); got.Code != http.StatusForbidden {
		t.Fatalf("reader label create = %d %s", got.Code, got.Body.String())
	}

	_, issue, err := e.issueService.CreateIssue(t.Context(), "acme", "widgets", authz.Authenticated(e.owner), models.NewIssue{Title: "protocol", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues/1/labels", `{"labels":["Bug"]}`, "reader", nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("reader label assignment = %d %s", response.Code, response.Body.String())
	}
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues/1/labels", `{"labels":["Bug"]}`, "owner", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("owner label assignment = %d %s", response.Code, response.Body.String())
	}

	comment, err := store.New(e.pool).Repo(e.scope.OrgID, e.scope.RepoID).CreateComment(t.Context(), models.NewComment{
		ID:          uuid.MustParse("60ea2b7a-854d-417d-9d83-a0262f4a13bb"),
		IssueNumber: 1,
		AuthorID:    &e.reader.User.ID,
		Body:        "comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	commentID := codec.StableNumericID(comment.Comment.ID.String())
	commentPath := fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", commentID)
	commentResponse := request(t, mux, http.MethodGet, commentPath, "", "reader", nil)
	commentETag := commentResponse.Header().Get("ETag")
	var browserComment map[string]any
	decode(t, commentResponse, &browserComment)
	browserCommentID, ok := browserComment["id"].(float64)
	if !ok || browserCommentID != float64(commentID) || int64(browserCommentID) != commentID ||
		commentID <= 0 || commentID > codec.MaxSafeNumericID {
		t.Fatalf("browser comment ID lost precision: json=%v stable=%d", browserComment["id"], commentID)
	}
	listResponse := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues/1/comments", "", "reader", nil)
	listETag := listResponse.Header().Get("ETag")
	issueResponse := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues/1", "", "reader", nil)
	issueETag := issueResponse.Header().Get("ETag")

	before := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	reactionPath := fmt.Sprintf("/repos/acme/widgets/issues/comments/%.0f/reactions", browserCommentID)
	response = request(t, mux, http.MethodPost, reactionPath, `{"content":"eyes"}`, "reader", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("create reaction = %d %s", response.Code, response.Body.String())
	}
	var reaction codec.Reaction
	decode(t, response, &reaction)
	if reaction.ID <= 0 || reaction.User.Login != "reader" {
		t.Fatalf("reaction = %+v", reaction)
	}
	var storedReactionID int64
	if err := e.pool.QueryRow(t.Context(), `SELECT compatibility_id FROM comment_reactions WHERE comment_id = $1`, comment.Comment.ID).Scan(&storedReactionID); err != nil || storedReactionID != reaction.ID {
		t.Fatalf("reaction compatibility id = %d response=%d err=%v", storedReactionID, reaction.ID, err)
	}
	response = request(t, mux, http.MethodGet, reactionPath, "", "reader", nil)
	var browserReactions []map[string]any
	decode(t, response, &browserReactions)
	if response.Code != http.StatusOK || len(browserReactions) != 1 {
		t.Fatalf("browser reaction list = %d %+v", response.Code, browserReactions)
	}
	browserReactionID, ok := browserReactions[0]["id"].(float64)
	if !ok || browserReactionID != float64(reaction.ID) || int64(browserReactionID) != reaction.ID ||
		reaction.ID <= 0 || reaction.ID > codec.MaxSafeNumericID {
		t.Fatalf("browser reaction ID lost precision: json=%v stable=%d", browserReactions[0]["id"], reaction.ID)
	}
	afterCreate := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	before.assertDelta(t, afterCreate, 1)

	response = request(t, mux, http.MethodPost, reactionPath, `{"content":"eyes"}`, "reader", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("idempotent reaction = %d %s", response.Code, response.Body.String())
	}
	afterDuplicate := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	afterCreate.assertDelta(t, afterDuplicate, 0)

	headers := map[string]string{"If-None-Match": commentETag, "If-Modified-Since": time.Now().Add(24 * time.Hour).Format(http.TimeFormat)}
	response = request(t, mux, http.MethodGet, commentPath, "", "reader", headers)
	if response.Code != http.StatusOK {
		t.Fatalf("comment invalidation/conditional precedence = %d %s", response.Code, response.Body.String())
	}
	var updated codec.Comment
	decode(t, response, &updated)
	if updated.Reactions.TotalCount != 1 || updated.Reactions.Eyes != 1 {
		t.Fatalf("reaction summary = %+v", updated.Reactions)
	}
	if got := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues/1/comments", "", "reader", map[string]string{"If-None-Match": listETag}); got.Code != http.StatusOK {
		t.Fatalf("comment list was not invalidated: %d", got.Code)
	}
	if got := request(t, mux, http.MethodGet, "/repos/acme/widgets/issues/1", "", "reader", map[string]string{"If-None-Match": issueETag}); got.Code != http.StatusOK {
		t.Fatalf("issue was not invalidated: %d", got.Code)
	}

	deletePath := fmt.Sprintf("%s/%.0f", reactionPath, browserReactionID)
	if got := request(t, mux, http.MethodDelete, deletePath, "", "other-reader", nil); got.Code != http.StatusForbidden {
		t.Fatalf("other reader reaction delete = %d %s", got.Code, got.Body.String())
	}
	if got := request(t, mux, http.MethodDelete, strings.Replace(deletePath, "/widgets/", "/other/", 1), "", "owner", nil); got.Code != http.StatusNotFound {
		t.Fatalf("cross-repo reaction delete = %d %s", got.Code, got.Body.String())
	}
	if got := request(t, mux, http.MethodDelete, deletePath, "", "owner", nil); got.Code != http.StatusNoContent || got.Body.Len() != 0 {
		t.Fatalf("triage delete = %d %q", got.Code, got.Body.String())
	}
	afterDelete := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	afterDuplicate.assertDelta(t, afterDelete, 1)
	response = request(t, mux, http.MethodPost, reactionPath, `{"content":"heart"}`, "reader", nil)
	decode(t, response, &reaction)
	if got := request(t, mux, http.MethodDelete, fmt.Sprintf("%s/%d", reactionPath, reaction.ID), "", "reader", nil); got.Code != http.StatusNoContent {
		t.Fatalf("own reaction delete = %d %s", got.Code, got.Body.String())
	}

	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/collaborators/owner/permission", "", "reader", nil)
	var permission codec.Permission
	decode(t, response, &permission)
	if response.Code != http.StatusOK || permission.Permission != "admin" || permission.User.Login != "owner" {
		t.Fatalf("permission = %d %+v", response.Code, permission)
	}
	if got := request(t, mux, http.MethodGet, "/repos/acme/widgets/collaborators/owner/permission", "", "wrong-cap", nil); got.Code != http.StatusNotFound {
		t.Fatalf("repo-cap mismatch = %d %s", got.Code, got.Body.String())
	}
	hiddenUserID := insertUser(t, e.pool, "hidden-user")
	hiddenOrgID := uuid.New()
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission)
		VALUES ($1, 'hidden-org', 'Hidden', 'read')`, hiddenOrgID); err != nil {
		t.Fatal(err)
	}
	_ = insertRepository(t, e.pool, hiddenOrgID, "hidden-repo")
	insertMembership(t, e.pool, hiddenOrgID, hiddenUserID, "owner")
	if got := request(t, mux, http.MethodGet, "/repos/acme/widgets/collaborators/hidden-user/permission", "", "reader", nil); got.Code != http.StatusNotFound {
		t.Fatalf("private cross-tenant login enumeration = %d %s", got.Code, got.Body.String())
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE repos SET visibility = 'public' WHERE organization_id = $1 AND id = $2`, e.scope.OrgID, e.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	if got := request(t, mux, http.MethodGet, "/repos/acme/widgets/collaborators/hidden-user/permission", "", "", nil); got.Code != http.StatusNotFound {
		t.Fatalf("public cross-tenant login enumeration = %d %s", got.Code, got.Body.String())
	}
	if got := request(t, mux, http.MethodGet, "/repos/acme/widgets/subscription", "", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous subscription = %d", got.Code)
	}
	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/subscription", "", "reader", nil)
	var subscription codec.Subscription
	decode(t, response, &subscription)
	if response.Code != http.StatusOK || !subscription.Subscribed || strings.Contains(subscription.URL, "attacker") {
		t.Fatalf("subscription = %d %+v", response.Code, subscription)
	}
	subscriptionETag := response.Header().Get("ETag")
	subscriptionModified := response.Header().Get("Last-Modified")
	if subscriptionModified == "" {
		t.Fatal("subscription Last-Modified is missing")
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE repo_subscriptions SET reason = 'ignored',
		representation_version = representation_version + 1, updated_at = updated_at + interval '2 seconds'
		WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3`, e.scope.OrgID, e.scope.RepoID, e.reader.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE repos SET subscriptions_collection_version = subscriptions_collection_version + 1
		WHERE organization_id = $1 AND id = $2`, e.scope.OrgID, e.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/subscription", "", "reader",
		map[string]string{"If-None-Match": subscriptionETag, "If-Modified-Since": subscriptionModified})
	decode(t, response, &subscription)
	if response.Code != http.StatusOK || !subscription.Ignored || subscription.Subscribed {
		t.Fatalf("updated subscription = %d %+v", response.Code, subscription)
	}
	defaultReader := request(t, mux, http.MethodGet, "/repos/acme/widgets/subscription", "", "other-reader", nil)
	if defaultReader.Code != http.StatusOK || defaultReader.Header().Get("ETag") == response.Header().Get("ETag") {
		t.Fatalf("default subscription fingerprint = %d etag=%q existing=%q", defaultReader.Code,
			defaultReader.Header().Get("ETag"), response.Header().Get("ETag"))
	}

	response = request(t, mux, http.MethodGet, "/repos/acme/widgets/labels?per_page=1", "", "reader", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Link"), "https://api.example.test/") || strings.Contains(response.Header().Get("Link"), "attacker") {
		t.Fatalf("canonical labels link = %d %v", response.Code, response.Header())
	}
	wantReset := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC).Unix()
	if response.Header().Get("X-RateLimit-Reset") != fmt.Sprint(wantReset) {
		t.Fatalf("stable reset = %q want %d", response.Header().Get("X-RateLimit-Reset"), wantReset)
	}

	noOpBefore := readLabelVersions(t, e.pool, e.scope, issue.Issue.ID)
	response = request(t, mux, http.MethodPut, "/repos/acme/widgets/issues/1/labels", `{"labels":["Bug"]}`, "owner", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("no-op replace = %d %s", response.Code, response.Body.String())
	}
	if noOpAfter := readLabelVersions(t, e.pool, e.scope, issue.Issue.ID); noOpAfter != noOpBefore {
		t.Fatalf("no-op replace changed versions before=%+v after=%+v", noOpBefore, noOpAfter)
	}
	beforeLabelUpdate := readLabelVersions(t, e.pool, e.scope, issue.Issue.ID)
	if got := request(t, mux, http.MethodPatch, "/repos/acme/widgets/labels/Bug", `{"color":"abcdef"}`, "reader", nil); got.Code != http.StatusForbidden {
		t.Fatalf("reader label update = %d %s", got.Code, got.Body.String())
	}
	response = request(t, mux, http.MethodPatch, "/repos/acme/widgets/labels/Bug", `{"color":"abcdef"}`, "owner", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("update label = %d %s", response.Code, response.Body.String())
	}
	afterLabelUpdate := readLabelVersions(t, e.pool, e.scope, issue.Issue.ID)
	if afterLabelUpdate.IssueRepresentation != beforeLabelUpdate.IssueRepresentation+1 ||
		afterLabelUpdate.IssueLabels != beforeLabelUpdate.IssueLabels+1 ||
		afterLabelUpdate.RepoIssues != beforeLabelUpdate.RepoIssues+1 ||
		afterLabelUpdate.RepoLabels != beforeLabelUpdate.RepoLabels+1 {
		t.Fatalf("label update versions before=%+v after=%+v", beforeLabelUpdate, afterLabelUpdate)
	}
	rollbackBefore := readLabelVersions(t, e.pool, e.scope, issue.Issue.ID)
	response = request(t, mux, http.MethodPut, "/repos/acme/widgets/issues/1/labels", `{"labels":["missing"]}`, "owner", nil)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown label = %d %s", response.Code, response.Body.String())
	}
	if rollbackAfter := readLabelVersions(t, e.pool, e.scope, issue.Issue.ID); rollbackAfter != rollbackBefore {
		t.Fatalf("unknown label changed versions before=%+v after=%+v", rollbackBefore, rollbackAfter)
	}
	permissionBefore := request(t, mux, http.MethodGet, "/repos/acme/widgets/collaborators/other-reader/permission", "", "reader", nil)
	if permissionBefore.Header().Get("Last-Modified") != "" {
		t.Fatalf("permission emitted unsafe Last-Modified: %q", permissionBefore.Header().Get("Last-Modified"))
	}
	if _, err := e.pool.Exec(t.Context(), `INSERT INTO repo_collaborators
		(organization_id, repository_id, user_id, role) VALUES ($1, $2, $3, 'write')`,
		e.scope.OrgID, e.scope.RepoID, e.bearer.principals["other-reader"].User.ID); err != nil {
		t.Fatal(err)
	}
	permissionAfter := request(t, mux, http.MethodGet, "/repos/acme/widgets/collaborators/other-reader/permission", "", "reader",
		map[string]string{"If-Modified-Since": time.Now().Add(24 * time.Hour).Format(http.TimeFormat)})
	decode(t, permissionAfter, &permission)
	if permissionAfter.Code != http.StatusOK || permission.Permission != "write" {
		t.Fatalf("permission authority update = %d %+v", permissionAfter.Code, permission)
	}
}

func TestConcurrentLabelAndReactionIdempotency(t *testing.T) {
	e := newEnvironment(t)
	subject := authz.Authenticated(e.owner)
	const workers = 16
	var wait sync.WaitGroup
	created := make(chan bool, workers)
	errorsCh := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := e.labelService.Create(context.Background(), "acme", "widgets", subject,
				models.NewLabel{Name: "Concurrent", Color: "123456"})
			if err == nil {
				created <- true
			} else if !errors.Is(err, store.ErrLabelAlreadyExists) {
				errorsCh <- err
			}
		}()
	}
	wait.Wait()
	close(created)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created labels = %d", len(created))
	}

	_, issue, err := e.issueService.CreateIssue(t.Context(), "acme", "widgets", subject, models.NewIssue{Title: "race", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	_, comment, err := e.issueService.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number, subject, "comment")
	if err != nil {
		t.Fatal(err)
	}
	commentID := codec.StableNumericID(comment.Comment.ID.String())
	before := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	reactionCreated := make(chan bool, workers)
	reactionErrors := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, result, err := e.reactionService.Create(context.Background(), "acme", "widgets", commentID, subject, "heart")
			if err != nil {
				reactionErrors <- err
				return
			}
			if result.Created {
				reactionCreated <- true
			}
		}()
	}
	wait.Wait()
	close(reactionCreated)
	close(reactionErrors)
	for err := range reactionErrors {
		t.Fatal(err)
	}
	if len(reactionCreated) != 1 {
		t.Fatalf("created reactions = %d", len(reactionCreated))
	}
	after := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	before.assertDelta(t, after, 1)
}

func TestReactionVersionFailureRollsBackSourceAndVersions(t *testing.T) {
	e := newEnvironment(t)
	subject := authz.Authenticated(e.owner)
	_, issue, err := e.issueService.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "rollback", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	_, comment, err := e.issueService.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number, subject, "comment")
	if err != nil {
		t.Fatal(err)
	}
	before := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	if _, err := e.pool.Exec(t.Context(), `CREATE FUNCTION reject_protocol_reaction_bump() RETURNS trigger
		LANGUAGE plpgsql AS $body$ BEGIN
			IF NEW.reactions_collection_version > OLD.reactions_collection_version THEN
				RAISE EXCEPTION 'injected reaction version failure';
			END IF;
			RETURN NEW;
		END $body$;
		CREATE TRIGGER reject_protocol_reaction_bump BEFORE UPDATE ON repos
		FOR EACH ROW EXECUTE FUNCTION reject_protocol_reaction_bump()`); err != nil {
		t.Fatal(err)
	}
	commentID := codec.StableNumericID(comment.Comment.ID.String())
	if _, _, err := e.reactionService.Create(t.Context(), "acme", "widgets", commentID, subject, "rocket"); err == nil {
		t.Fatal("reaction unexpectedly committed through injected version failure")
	}
	after := readVersions(t, e.pool, e.scope, comment.Comment.ID, issue.Issue.ID)
	before.assertDelta(t, after, 0)
	var count int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM comment_reactions WHERE comment_id = $1`, comment.Comment.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back reactions = %d, %v", count, err)
	}
}

type environment struct {
	pool            *pgxpool.Pool
	scope           models.RepoScope
	otherScope      models.RepoScope
	owner           serverauth.Principal
	reader          serverauth.Principal
	issueService    *issueapi.Service
	labelService    *labelapi.Service
	reactionService *reactionapi.Service
	permission      *permissionapi.Service
	subscription    *subscriptionapi.Service
	bearer          *testBearer
	origins         publicurl.Origins
	conditional     conditional.Policy
}

func newEnvironment(t *testing.T) *environment {
	pool := migratedPool(t)
	ownerID := insertUser(t, pool, "owner")
	readerID := insertUser(t, pool, "reader")
	otherReaderID := insertUser(t, pool, "other-reader")
	orgID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO orgs (id, name, display_name, base_permission)
		VALUES ($1, 'acme', 'Acme', 'read')`, orgID); err != nil {
		t.Fatal(err)
	}
	scope := insertRepository(t, pool, orgID, "widgets")
	otherScope := insertRepository(t, pool, orgID, "other")
	insertMembership(t, pool, orgID, ownerID, "owner")
	insertMembership(t, pool, orgID, readerID, "reader")
	insertMembership(t, pool, orgID, otherReaderID, "reader")
	owner := principal(t, pool, ownerID, "owner")
	reader := principal(t, pool, readerID, "reader")
	otherReader := principal(t, pool, otherReaderID, "other-reader")
	wrongCap := serverauth.Principal{User: owner.User, Kind: serverauth.CredentialPAT,
		Scopes: []string{"issues:read"}, RepoRestricted: true,
		RepositoryCaps: []serverauth.RepositoryCap{{OrgID: otherScope.OrgID, RepoID: otherScope.RepoID}}}
	authorizer, err := authz.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	issueService, err := issueapi.NewService(store.New(pool), authorizer, artifacts.MarkerProjector{}, noopHook{})
	if err != nil {
		t.Fatal(err)
	}
	labelService, _ := labelapi.NewService(store.New(pool), authorizer)
	reactionService, _ := reactionapi.NewService(store.New(pool), authorizer)
	permission, _ := permissionapi.NewService(store.New(pool), authorizer)
	subscription, _ := subscriptionapi.NewService(store.New(pool), authorizer)
	if _, err := pool.Exec(t.Context(), `INSERT INTO repo_subscriptions
		(organization_id, repository_id, user_id, reason) VALUES ($1, $2, $3, 'manual')`, orgID, scope.RepoID, readerID); err != nil {
		t.Fatal(err)
	}
	origins, _ := publicurl.New("https://api.example.test", "https://web.example.test", nil)
	policy := conditional.Policy{Clock: func() time.Time { return time.Date(2026, 7, 11, 3, 17, 0, 0, time.UTC) }}
	return &environment{pool: pool, scope: scope, otherScope: otherScope, owner: owner, reader: reader,
		issueService: issueService, labelService: labelService, reactionService: reactionService,
		permission: permission, subscription: subscription, origins: origins, conditional: policy,
		bearer: &testBearer{principals: map[string]serverauth.Principal{
			"owner": owner, "reader": reader, "other-reader": otherReader, "wrong-cap": wrongCap,
		}}}
}

func (e *environment) mux(t *testing.T) http.Handler {
	authentication := serverauth.Middleware{Bearer: e.bearer}
	issueRoutes, _ := issueapi.NewRouteSet(issueapi.Dependencies{Service: e.issueService, Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication, Conditional: e.conditional})
	commentRoutes, _ := commentapi.NewRouteSet(commentapi.Dependencies{Service: e.issueService, Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication, Conditional: e.conditional})
	labelRoutes, _ := labelapi.NewRouteSet(labelapi.Dependencies{Service: e.labelService, Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication, Conditional: e.conditional})
	reactionRoutes, _ := reactionapi.NewRouteSet(reactionapi.Dependencies{Service: e.reactionService, Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication, Conditional: e.conditional})
	permissionRoutes, _ := permissionapi.NewRouteSet(permissionapi.Dependencies{Service: e.permission, Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication, Conditional: e.conditional})
	subscriptionRoutes, _ := subscriptionapi.NewRouteSet(subscriptionapi.Dependencies{Service: e.subscription, Presenter: codec.Presenter{Origins: e.origins}, Authentication: authentication, Conditional: e.conditional})
	mux, err := routeset.NewMux(routeset.SelfHostedPolicy(), issueRoutes, commentRoutes, labelRoutes, reactionRoutes, permissionRoutes, subscriptionRoutes)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

type noopHook struct{}

func (noopHook) Emit(context.Context, store.RepoStore, issueapi.MutationEvent) error { return nil }

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

type versions struct {
	CommentRepresentation int64
	CommentReactions      int64
	IssueComments         int64
	RepoIssues            int64
	RepoComments          int64
	RepoReactions         int64
}

func readVersions(t *testing.T, pool *pgxpool.Pool, scope models.RepoScope, commentID, issueID uuid.UUID) versions {
	t.Helper()
	var result versions
	if err := pool.QueryRow(t.Context(), `SELECT c.representation_version, c.reactions_collection_version,
		i.comments_collection_version, r.issues_collection_version, r.comments_collection_version,
		r.reactions_collection_version FROM comments c JOIN issues i ON i.id = c.issue_id
		JOIN repos r ON r.organization_id = c.organization_id AND r.id = c.repository_id
		WHERE c.organization_id = $1 AND c.repository_id = $2 AND c.id = $3 AND i.id = $4`,
		scope.OrgID, scope.RepoID, commentID, issueID).Scan(&result.CommentRepresentation,
		&result.CommentReactions, &result.IssueComments, &result.RepoIssues,
		&result.RepoComments, &result.RepoReactions); err != nil {
		t.Fatal(err)
	}
	return result
}

func (v versions) assertDelta(t *testing.T, after versions, delta int64) {
	t.Helper()
	want := versions{v.CommentRepresentation + delta, v.CommentReactions + delta,
		v.IssueComments + delta, v.RepoIssues + delta, v.RepoComments + delta, v.RepoReactions + delta}
	if after != want {
		t.Fatalf("versions before=%+v after=%+v want=%+v", v, after, want)
	}
}

type labelVersions struct{ IssueRepresentation, IssueLabels, RepoIssues, RepoLabels int64 }

func readLabelVersions(t *testing.T, pool *pgxpool.Pool, scope models.RepoScope, issueID uuid.UUID) labelVersions {
	t.Helper()
	var result labelVersions
	if err := pool.QueryRow(t.Context(), `SELECT i.representation_version, i.labels_collection_version,
		r.issues_collection_version, r.labels_collection_version FROM issues i
		JOIN repos r ON r.organization_id = i.organization_id AND r.id = i.repository_id
		WHERE i.organization_id = $1 AND i.repository_id = $2 AND i.id = $3`,
		scope.OrgID, scope.RepoID, issueID).Scan(&result.IssueRepresentation,
		&result.IssueLabels, &result.RepoIssues, &result.RepoLabels); err != nil {
		t.Fatal(err)
	}
	return result
}

func request(t *testing.T, handler http.Handler, method, path, body, bearer string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Host = "attacker.invalid"
	r.Header.Set("Forwarded", "host=attacker.invalid;proto=http")
	r.Header.Set("X-Forwarded-Host", "attacker.invalid")
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, r)
	return response
}

func decode(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
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
	schema := "protocol_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE") })
	config, _ := pgxpool.ParseConfig(databaseURL)
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
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $2)`, id, login); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRepository(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, name string) models.RepoScope {
	repoID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility, contribution_policy)
		VALUES ($1, $2, $3, $3, 'private', 'members')`, repoID, orgID, name); err != nil {
		t.Fatal(err)
	}
	return models.RepoScope{OrgID: orgID, RepoID: repoID}
}

func insertMembership(t *testing.T, pool *pgxpool.Pool, orgID, userID uuid.UUID, role string) {
	if _, err := pool.Exec(t.Context(), `INSERT INTO org_memberships
		(organization_id, user_id, role, state, activated_at) VALUES ($1, $2, $3, 'active', clock_timestamp())`,
		orgID, userID, role); err != nil {
		t.Fatal(err)
	}
}

func principal(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, login string) serverauth.Principal {
	sessionID := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, idle_expires_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour', clock_timestamp() + interval '2 hours')`,
		sessionID, userID, "session-"+sessionID.String(), []byte(sessionID.String()), []byte("csrf")); err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{User: serverauth.User{ID: userID, Login: login, Status: "active"},
		Kind: serverauth.CredentialSession, CredentialID: sessionID}
}
