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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/preview"
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
	"github.com/higress-group/issue-spec/internal/templates"
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

	repositoryResponse := request(t, mux, http.MethodGet, "/repos/acme/widgets", "", "")
	var repository codec.Repository
	decode(t, repositoryResponse, &repository)
	if repositoryResponse.Code != http.StatusOK || repository.FullName != "acme/widgets" ||
		repository.URL != "https://api.example.test/repos/acme/widgets" ||
		repository.HTMLURL != "https://web.example.test/acme/widgets" ||
		repository.Owner.Type != "Organization" || repository.Owner.HTMLURL != "" {
		t.Fatalf("repository resource status=%d value=%+v", repositoryResponse.Code, repository)
	}

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

func TestIssueCommentDeleteAuthorizationProjectionAndReadConvergence(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPublic)
	mux := environment.mux(t)
	subject := authz.Authenticated(environment.owner)
	_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "comment deletion", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	author := environment.addMember(t, "comment-author", "reader")
	other := environment.addMember(t, "comment-other", "reader")
	typed := "<!-- issue-spec:type=SPEC id=SPEC-777 version=1 -->\nAgent: Worker\nType: SPEC\nID: SPEC-777\nStatus: confirmed\nScope: delete\nLinks:\n\n## Requirement:\nDelete safely.\n"
	_, comment, err := environment.service.CreateComment(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(author), typed)
	if err != nil {
		t.Fatal(err)
	}
	commentID := codec.StableNumericID(comment.Comment.ID.String())
	path := fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", commentID)
	assertProjectionCounts(t, environment.pool, 1, 0)

	type versions struct {
		issues, comments, artifacts, issueComments int64
	}
	readVersions := func() versions {
		t.Helper()
		var result versions
		if err := environment.pool.QueryRow(t.Context(), `SELECT r.issues_collection_version,
			r.comments_collection_version, r.artifacts_collection_version,
			i.comments_collection_version
			FROM repos r JOIN issues i ON i.organization_id = r.organization_id
				AND i.repository_id = r.id
			WHERE r.organization_id = $1 AND r.id = $2 AND i.id = $3`,
			environment.scope.OrgID, environment.scope.RepoID, issue.Issue.ID).
			Scan(&result.issues, &result.comments, &result.artifacts, &result.issueComments); err != nil {
			t.Fatal(err)
		}
		return result
	}
	beforeDenied := readVersions()
	denied := request(t, mux, http.MethodDelete, path, "", other.User.Login)
	if denied.Code != http.StatusForbidden || countRows(t, environment.pool, "comments") != 1 ||
		readVersions() != beforeDenied {
		t.Fatalf("denied delete status=%d body=%s versions=%+v/%+v comments=%d",
			denied.Code, denied.Body.String(), beforeDenied, readVersions(),
			countRows(t, environment.pool, "comments"))
	}
	if invalid := request(t, mux, http.MethodDelete,
		"/repos/acme/widgets/issues/comments/0", "", author.User.Login); invalid.Code != http.StatusNotFound {
		t.Fatalf("invalid delete status=%d body=%s", invalid.Code, invalid.Body.String())
	}

	success := request(t, mux, http.MethodDelete, path, "", author.User.Login)
	if success.Code != http.StatusNoContent || success.Body.Len() != 0 {
		t.Fatalf("author delete status=%d body=%q", success.Code, success.Body.String())
	}
	after := readVersions()
	if after.issues != beforeDenied.issues+1 || after.comments != beforeDenied.comments+1 ||
		after.artifacts != beforeDenied.artifacts+1 || after.issueComments != beforeDenied.issueComments+1 {
		t.Fatalf("delete versions before=%+v after=%+v", beforeDenied, after)
	}
	assertProjectionCounts(t, environment.pool, 0, 0)
	if direct := request(t, mux, http.MethodGet, path, "", ""); direct.Code != http.StatusNotFound {
		t.Fatalf("deleted direct read status=%d body=%s", direct.Code, direct.Body.String())
	}
	list := request(t, mux, http.MethodGet,
		fmt.Sprintf("/repos/acme/widgets/issues/%d/comments?per_page=100", issue.Issue.Number), "", "")
	var comments []codec.Comment
	decode(t, list, &comments)
	if list.Code != http.StatusOK || len(comments) != 0 {
		t.Fatalf("deleted comment list status=%d comments=%+v", list.Code, comments)
	}
	issueRead := request(t, mux, http.MethodGet,
		fmt.Sprintf("/repos/acme/widgets/issues/%d", issue.Issue.Number), "", "")
	var issueDTO codec.Issue
	decode(t, issueRead, &issueDTO)
	if issueRead.Code != http.StatusOK || issueDTO.Comments != 0 {
		t.Fatalf("issue after delete status=%d issue=%+v", issueRead.Code, issueDTO)
	}

	_, triageTarget, err := environment.service.CreateComment(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(author), "triager may delete this")
	if err != nil {
		t.Fatal(err)
	}
	triageDelete := request(t, mux, http.MethodDelete,
		fmt.Sprintf("/repos/acme/widgets/issues/comments/%d",
			codec.StableNumericID(triageTarget.Comment.ID.String())), "", "owner")
	if triageDelete.Code != http.StatusNoContent {
		t.Fatalf("triager delete status=%d body=%s", triageDelete.Code, triageDelete.Body.String())
	}
}

func TestTrustedPreviewServiceRevalidatesExactCurrentStoredSource(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPublic)
	subject := authz.Authenticated(environment.owner)
	issueBody := "before\n```html-preview id=review version=1\n<!doctype html><p>issue source</p>\n```\nafter\n"
	_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "preview", Body: issueBody})
	if err != nil {
		t.Fatal(err)
	}
	issueSelection, err := preview.Select(issueBody, "review")
	if err != nil {
		t.Fatal(err)
	}
	document, err := environment.service.PreviewDocument(t.Context(), "acme", "widgets", issue.Issue.Number,
		authz.Anonymous(), issueapi.PreviewSource{Kind: issueapi.PreviewSourceIssue}, "review",
		issueSelection.Descriptor.Digest)
	if err != nil || document != issueSelection.Source {
		t.Fatalf("public exact issue preview=%q err=%v", document, err)
	}

	commentBody := "```html-preview id=comment-review version=1\n<script>document.body.textContent='comment'</script>\n```\n"
	_, comment, err := environment.service.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number,
		subject, commentBody)
	if err != nil {
		t.Fatal(err)
	}
	commentSelection, err := preview.Select(commentBody, "comment-review")
	if err != nil {
		t.Fatal(err)
	}
	commentID := codec.StableNumericID(comment.Comment.ID.String())
	document, err = environment.service.PreviewDocument(t.Context(), "acme", "widgets", issue.Issue.Number,
		authz.Anonymous(), issueapi.PreviewSource{Kind: issueapi.PreviewSourceComment, CommentID: commentID},
		"comment-review", commentSelection.Descriptor.Digest)
	if err != nil || document != commentSelection.Source {
		t.Fatalf("public exact comment preview=%q err=%v", document, err)
	}
	if _, err := environment.service.PreviewDocument(t.Context(), "acme", "widgets", issue.Issue.Number,
		authz.Anonymous(), issueapi.PreviewSource{Kind: issueapi.PreviewSourceComment, CommentID: commentID},
		"comment-review", strings.Repeat("0", 64)); !errors.Is(err, issueapi.ErrPreviewDigestMismatch) {
		t.Fatalf("stale digest error=%v", err)
	}

	duplicate := "```html-preview id=dup version=1\none\n```\n```html-preview id=dup version=1\ntwo\n```\n"
	_, duplicateIssue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "duplicate", Body: duplicate})
	if err != nil {
		t.Fatal(err)
	}
	duplicateDigest := preview.Parse(duplicate).Descriptors[0].Digest
	if _, err := environment.service.PreviewDocument(t.Context(), "acme", "widgets",
		duplicateIssue.Issue.Number, authz.Anonymous(), issueapi.PreviewSource{Kind: issueapi.PreviewSourceIssue},
		"dup", duplicateDigest); !errors.Is(err, issueapi.ErrInvalidPreviewRequest) {
		t.Fatalf("duplicate source error=%v", err)
	}
	for _, invalid := range []struct {
		title string
		body  string
		id    string
	}{
		{title: "malformed", body: "```html-preview id=bad\nmalformed\n```\n", id: "bad"},
		{title: "oversized", body: "```html-preview id=huge version=1\n" +
			strings.Repeat("x", preview.MaxSourceSize+1) + "\n```\n", id: "huge"},
	} {
		_, invalidIssue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
			models.NewIssue{Title: invalid.title, Body: invalid.body})
		if err != nil {
			t.Fatal(err)
		}
		digest := preview.Parse(invalid.body).Descriptors[0].Digest
		if _, err := environment.service.PreviewDocument(t.Context(), "acme", "widgets",
			invalidIssue.Issue.Number, authz.Anonymous(),
			issueapi.PreviewSource{Kind: issueapi.PreviewSourceIssue}, invalid.id, digest); !errors.Is(err, issueapi.ErrInvalidPreviewRequest) {
			t.Fatalf("%s source error=%v", invalid.title, err)
		}
	}

	updatedBody := strings.Replace(issueBody, "issue source", "changed source", 1)
	if _, _, err := environment.service.UpdateIssue(t.Context(), "acme", "widgets", issue.Issue.Number,
		subject, func(current models.Issue) (models.IssueUpdate, error) {
			return models.IssueUpdate{Title: current.Title, Body: updatedBody, State: current.State}, nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.service.PreviewDocument(t.Context(), "acme", "widgets", issue.Issue.Number,
		authz.Anonymous(), issueapi.PreviewSource{Kind: issueapi.PreviewSourceIssue}, "review",
		issueSelection.Descriptor.Digest); !errors.Is(err, issueapi.ErrPreviewDigestMismatch) {
		t.Fatalf("updated source stale digest error=%v", err)
	}

	privateEnvironment := newEnvironment(t, models.VisibilityPrivate)
	_, privateIssue, err := privateEnvironment.service.CreateIssue(t.Context(), "acme", "widgets",
		authz.Authenticated(privateEnvironment.owner), models.NewIssue{Title: "private", Body: issueBody})
	if err != nil {
		t.Fatal(err)
	}
	_, err = privateEnvironment.service.PreviewDocument(t.Context(), "acme", "widgets",
		privateIssue.Issue.Number, authz.Anonymous(), issueapi.PreviewSource{Kind: issueapi.PreviewSourceIssue},
		"review", issueSelection.Descriptor.Digest)
	var denied *issueapi.DecisionError
	if !errors.As(err, &denied) || denied.Decision.Visible {
		t.Fatalf("anonymous private preview error=%v", err)
	}
}

func TestTrustedAnswerServiceAppendsCanonicalImmutableAnswersAtomically(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	subject := authz.Authenticated(environment.owner)
	const webOrigin = "http://web.example.test"
	_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "answer", Body: "authoritative issue"})
	if err != nil {
		t.Fatal(err)
	}
	questionBody := trustedQuestionBody(t, "QUESTION-007", "Choose one?",
		[]model.ChoiceOption{{ID: "safe", Label: "Safe"}, {ID: "fast", Label: "Fast"}})
	_, questionComment, err := environment.service.CreateComment(t.Context(), "acme", "widgets",
		issue.Issue.Number, subject, questionBody)
	if err != nil {
		t.Fatal(err)
	}
	question, err := func() (issueapi.QuestionAuthority, error) {
		_, value, err := environment.service.GetQuestion(t.Context(), "acme", "widgets",
			issue.Issue.Number, subject, webOrigin, "QUESTION-007")
		return value, err
	}()
	if err != nil || question.Snapshot.Question != "Choose one?" ||
		question.RepresentationVersion != questionComment.Comment.RepresentationVersion ||
		question.BodyDigest != model.RepresentationDigest(questionBody) ||
		!strings.HasPrefix(question.Snapshot.SourceURL, webOrigin+"/") ||
		!strings.Contains(question.Snapshot.SourceURL, "#issuecomment-") {
		t.Fatalf("question authority=%+v err=%v", question, err)
	}

	var answers []models.CommentSnapshot
	for answerIndex := range 2 {
		_, answer, usedQuestion, err := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
			issue.Issue.Number, subject, webOrigin,
			issueapi.AnswerIntent{QuestionID: "QUESTION-007", QuestionDigest: question.BodyDigest,
				OptionIDs: []string{"safe"}})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := model.ParseAnswerPayload(answer.Comment.Body)
		parsed := model.ParseTypedComment(answer.Comment.Body)
		wantAnswerID := fmt.Sprintf("ANSWER-%d%03d", issue.Issue.Number, answerIndex+1)
		if err != nil || !reflect.DeepEqual(payload.Question, usedQuestion.Snapshot) || payload.Selection.Options[0].ID != "safe" ||
			parsed.Type != "ANSWER" || parsed.Status != "done" || parsed.Agent != environment.owner.User.Login ||
			parsed.ID != wantAnswerID || model.ValidateIssueScopedTypedIdentity("ANSWER", parsed.ID, issue.Issue.Number) != nil ||
			parsed.AgentSessionID != "" || answer.Comment.AuthorID == nil ||
			*answer.Comment.AuthorID != environment.owner.User.ID ||
			answer.Comment.CreatedAt.IsZero() || answer.Comment.UpdatedAt.Before(answer.Comment.CreatedAt) ||
			answer.Comment.RepresentationVersion != 1 {
			t.Fatalf("answer=%+v parsed=%+v payload=%+v err=%v", answer, parsed, payload, err)
		}
		answers = append(answers, answer)
	}
	if answers[0].Comment.ID == answers[1].Comment.ID ||
		model.ParseTypedComment(answers[0].Comment.Body).ID == model.ParseTypedComment(answers[1].Comment.Body).ID {
		t.Fatalf("repeated submission was not append-only: %+v", answers)
	}
	if _, _, err := environment.service.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number,
		subject, answers[0].Comment.Body); !errors.Is(err, issueapi.ErrTrustedAnswerRequired) {
		t.Fatalf("generic ANSWER create error=%v", err)
	}
	if _, _, err := environment.service.UpdateComment(t.Context(), "acme", "widgets",
		codec.StableNumericID(answers[0].Comment.ID.String()), subject, "rewritten"); !errors.Is(err, issueapi.ErrAnswerImmutable) {
		t.Fatalf("ANSWER patch error=%v", err)
	}

	changedQuestion := trustedQuestionBody(t, "QUESTION-007", "Choose again?",
		[]model.ChoiceOption{{ID: "fast", Label: "Fast"}})
	if _, _, err := environment.service.UpdateComment(t.Context(), "acme", "widgets",
		codec.StableNumericID(questionComment.Comment.ID.String()), subject, changedQuestion); err != nil {
		t.Fatal(err)
	}
	beforeComments := countRows(t, environment.pool, "comments")
	if _, _, _, err := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
		issue.Issue.Number, subject, webOrigin,
		issueapi.AnswerIntent{QuestionID: "QUESTION-007", QuestionDigest: question.BodyDigest,
			OptionIDs: []string{"safe"}}); !errors.Is(err, issueapi.ErrQuestionChanged) || countRows(t, environment.pool, "comments") != beforeComments {
		t.Fatalf("stale intent error=%v comments=%d/%d", err, beforeComments, countRows(t, environment.pool, "comments"))
	}
	changedDigest := model.RepresentationDigest(changedQuestion)

	beforeTyped := countRows(t, environment.pool, "issue_spec_typed_comments")
	beforeOutbox := countRows(t, environment.pool, "event_outbox")
	var issueCommentVersion int64
	if err := environment.pool.QueryRow(t.Context(), `SELECT comments_collection_version FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3`,
		environment.scope.OrgID, environment.scope.RepoID, issue.Issue.Number).Scan(&issueCommentVersion); err != nil {
		t.Fatal(err)
	}
	environment.hook.fail.Store(true)
	_, _, _, failed := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
		issue.Issue.Number, subject, webOrigin,
		issueapi.AnswerIntent{QuestionID: "QUESTION-007", QuestionDigest: changedDigest,
			OptionIDs: []string{"fast"}})
	environment.hook.fail.Store(false)
	var currentCommentVersion int64
	if err := environment.pool.QueryRow(t.Context(), `SELECT comments_collection_version FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3`,
		environment.scope.OrgID, environment.scope.RepoID, issue.Issue.Number).Scan(&currentCommentVersion); err != nil {
		t.Fatal(err)
	}
	if failed == nil || countRows(t, environment.pool, "comments") != beforeComments ||
		countRows(t, environment.pool, "issue_spec_typed_comments") != beforeTyped ||
		countRows(t, environment.pool, "event_outbox") != beforeOutbox ||
		currentCommentVersion != issueCommentVersion {
		t.Fatalf("failed answer was not atomic: err=%v comments=%d/%d typed=%d/%d outbox=%d/%d version=%d/%d",
			failed, beforeComments, countRows(t, environment.pool, "comments"), beforeTyped,
			countRows(t, environment.pool, "issue_spec_typed_comments"), beforeOutbox,
			countRows(t, environment.pool, "event_outbox"), issueCommentVersion, currentCommentVersion)
	}

	outsider := environment.addOutsider(t, "outsider")
	if _, _, _, err := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(outsider), webOrigin,
		issueapi.AnswerIntent{QuestionID: "QUESTION-007", QuestionDigest: changedDigest,
			OptionIDs: []string{"fast"}}); err == nil {
		t.Fatal("private repository outsider created ANSWER")
	}
}

func TestTrustedAnswerServiceRevalidatesPATScopeRepositoryAndIntentAtomically(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	sessionSubject := authz.Authenticated(environment.owner)
	const webOrigin = "http://web.example.test"
	_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", sessionSubject,
		models.NewIssue{Title: "PAT answer", Body: "authoritative issue"})
	if err != nil {
		t.Fatal(err)
	}
	questionBody := trustedQuestionBody(t, "QUESTION-007", "Choose one?",
		[]model.ChoiceOption{{ID: "safe", Label: "Safe"}, {ID: "fast", Label: "Fast"}})
	if _, _, err := environment.service.CreateComment(t.Context(), "acme", "widgets",
		issue.Issue.Number, sessionSubject, questionBody); err != nil {
		t.Fatal(err)
	}

	writePAT := insertPAT(t, environment.pool, environment.owner, "answer-write",
		[]string{"issues:write"}, []models.RepoScope{environment.scope})
	readPAT := insertPAT(t, environment.pool, environment.owner, "answer-read",
		[]string{"issues:read"}, []models.RepoScope{environment.scope})
	noScopePAT := insertPAT(t, environment.pool, environment.owner, "answer-no-scope",
		nil, []models.RepoScope{environment.scope})
	_, question, err := environment.service.GetQuestion(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(readPAT), webOrigin, "QUESTION-007")
	if err != nil || question.BodyDigest != model.RepresentationDigest(questionBody) {
		t.Fatalf("read-scoped PAT question=%+v err=%v", question, err)
	}
	if _, _, err := environment.service.GetQuestion(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(noScopePAT), webOrigin, "QUESTION-007"); err == nil {
		t.Fatal("scope-less PAT read QUESTION")
	}

	_, selected, _, err := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(writePAT), webOrigin,
		issueapi.AnswerIntent{QuestionID: "QUESTION-007", QuestionDigest: question.BodyDigest,
			OptionIDs: []string{"safe"}})
	if err != nil {
		t.Fatal(err)
	}
	selectedPayload, err := model.ParseAnswerPayload(selected.Comment.Body)
	selectedTyped := model.ParseTypedComment(selected.Comment.Body)
	if err != nil || len(selectedPayload.Selection.Options) != 1 ||
		selectedPayload.Selection.Options[0].ID != "safe" || selectedTyped.Type != "ANSWER" ||
		selectedTyped.Agent != environment.owner.User.Login || selectedTyped.AgentSessionID != "" ||
		selected.Comment.AuthorID == nil || *selected.Comment.AuthorID != environment.owner.User.ID {
		t.Fatalf("selected PAT answer=%+v typed=%+v payload=%+v err=%v",
			selected, selectedTyped, selectedPayload, err)
	}

	_, custom, _, err := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
		issue.Issue.Number, authz.Authenticated(writePAT), webOrigin,
		issueapi.AnswerIntent{QuestionID: "QUESTION-007", QuestionDigest: question.BodyDigest,
			Custom: "Use the audited alternative."})
	if err != nil {
		t.Fatal(err)
	}
	customPayload, err := model.ParseAnswerPayload(custom.Comment.Body)
	customTyped := model.ParseTypedComment(custom.Comment.Body)
	if err != nil || customPayload.Selection.Custom != "Use the audited alternative." ||
		len(customPayload.Selection.Options) != 0 || customTyped.Type != "ANSWER" ||
		customTyped.Agent != environment.owner.User.Login || customTyped.AgentSessionID != "" ||
		custom.Comment.ID == selected.Comment.ID || customTyped.ID == selectedTyped.ID {
		t.Fatalf("custom PAT answer=%+v typed=%+v payload=%+v err=%v",
			custom, customTyped, customPayload, err)
	}

	assertNoAppend := func(name string, call func() error, match func(error) bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			before := countRows(t, environment.pool, "comments")
			err := call()
			if !match(err) || countRows(t, environment.pool, "comments") != before {
				t.Fatalf("error=%v comments=%d/%d", err, before,
					countRows(t, environment.pool, "comments"))
			}
		})
	}
	isInvalidIntent := func(err error) bool { return errors.Is(err, issueapi.ErrInvalidAnswerIntent) }
	isChanged := func(err error) bool { return errors.Is(err, issueapi.ErrQuestionChanged) }
	deniedWithVisibility := func(visible bool) func(error) bool {
		return func(err error) bool {
			var denied *issueapi.DecisionError
			return errors.As(err, &denied) && denied.Decision.Visible == visible
		}
	}
	create := func(principal serverauth.Principal, intent issueapi.AnswerIntent) func() error {
		return func() error {
			_, _, _, err := environment.service.CreateAnswer(t.Context(), "acme", "widgets",
				issue.Issue.Number, authz.Authenticated(principal), webOrigin, intent)
			return err
		}
	}
	validIntent := issueapi.AnswerIntent{QuestionID: "QUESTION-007",
		QuestionDigest: question.BodyDigest, OptionIDs: []string{"safe"}}

	assertNoAppend("read scope cannot write", create(readPAT, validIntent), deniedWithVisibility(true))
	assertNoAppend("stale digest", create(writePAT, issueapi.AnswerIntent{
		QuestionID: "QUESTION-007", QuestionDigest: strings.Repeat("b", 64),
		OptionIDs: []string{"safe"},
	}), isChanged)
	assertNoAppend("malformed digest", create(writePAT, issueapi.AnswerIntent{
		QuestionID: "QUESTION-007", QuestionDigest: "not-a-digest", OptionIDs: []string{"safe"},
	}), isInvalidIntent)
	assertNoAppend("unknown option", create(writePAT, issueapi.AnswerIntent{
		QuestionID: "QUESTION-007", QuestionDigest: question.BodyDigest,
		OptionIDs: []string{"unknown"},
	}), isInvalidIntent)
	assertNoAppend("invalid custom input", create(writePAT, issueapi.AnswerIntent{
		QuestionID: "QUESTION-007", QuestionDigest: question.BodyDigest,
		Custom: strings.Repeat("x", 4*1024+1),
	}), isInvalidIntent)

	otherRepoID := uuid.New()
	if _, err := environment.pool.Exec(t.Context(), `INSERT INTO repos
		(id, organization_id, name, display_name, visibility, contribution_policy)
		VALUES ($1, $2, 'other-cap', 'other-cap', 'private', 'members')`,
		otherRepoID, environment.scope.OrgID); err != nil {
		t.Fatal(err)
	}
	wrongCapPAT := insertPAT(t, environment.pool, environment.owner, "answer-wrong-cap",
		[]string{"issues:write"},
		[]models.RepoScope{{OrgID: environment.scope.OrgID, RepoID: otherRepoID}})
	assertNoAppend("repository cap conceals target", create(wrongCapPAT, validIntent),
		deniedWithVisibility(false))

	outsider := environment.addOutsider(t, "pat-outsider")
	outsiderPAT := insertPAT(t, environment.pool, outsider, "answer-outsider",
		[]string{"issues:write"}, []models.RepoScope{environment.scope})
	assertNoAppend("private repository is invisible", create(outsiderPAT, validIntent),
		deniedWithVisibility(false))
	if _, err := environment.pool.Exec(t.Context(), `UPDATE repos SET visibility = 'public'
		WHERE organization_id = $1 AND id = $2`, environment.scope.OrgID, environment.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	assertNoAppend("visible repository still requires contribution authority",
		create(outsiderPAT, validIntent), deniedWithVisibility(true))

	if _, err := environment.pool.Exec(t.Context(), `DELETE FROM pat_scopes
		WHERE personal_access_token_id = $1`, writePAT.CredentialID); err != nil {
		t.Fatal(err)
	}
	assertNoAppend("live scope removal invalidates authenticated PAT",
		create(writePAT, validIntent), deniedWithVisibility(true))

	liveCapPAT := insertPAT(t, environment.pool, environment.owner, "answer-live-cap",
		[]string{"issues:write"}, []models.RepoScope{environment.scope})
	if _, err := environment.pool.Exec(t.Context(), `DELETE FROM pat_repositories
		WHERE personal_access_token_id = $1`, liveCapPAT.CredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.pool.Exec(t.Context(), `INSERT INTO pat_repositories
		(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`,
		liveCapPAT.CredentialID, environment.scope.OrgID, otherRepoID); err != nil {
		t.Fatal(err)
	}
	assertNoAppend("live repository cap change invalidates authenticated PAT",
		create(liveCapPAT, validIntent), deniedWithVisibility(false))
}

func trustedQuestionBody(t *testing.T, id, question string, options []model.ChoiceOption) string {
	t.Helper()
	choice := model.ChoiceModel{Version: model.ChoiceModelVersion, Mode: model.ChoiceModeSingle,
		AllowCustom: true, Options: options}
	body, err := templates.QuestionComment(templates.QuestionOptions{
		ID: id, Agent: "Coordinator", Status: "blocked", Scope: "trusted answer integration",
		Blocking: true, Question: question, Assumption: "Use safe.", ChoiceModel: &choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestPublicContributorIssueCompatibility(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPublic)
	if _, err := environment.pool.Exec(t.Context(), `UPDATE repos SET contribution_policy = 'public'
		WHERE id = $1`, environment.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	insertLabel(t, environment.pool, environment.scope, "issue-spec/proposal")
	insertLabel(t, environment.pool, environment.scope, "extra")
	contributor := environment.addOutsider(t, "contributor")
	other := environment.addOutsider(t, "other-contributor")
	mux := environment.mux(t)

	response := request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
		jsonBody(map[string]any{"title": "simple requirement", "body": "plain text"}), "contributor")
	if response.Code != http.StatusCreated {
		t.Fatalf("simple contribution status=%d body=%s", response.Code, response.Body.String())
	}
	var simple codec.Issue
	decode(t, response, &simple)
	if simple.User.Login != contributor.User.Login || len(simple.Labels) != 0 {
		t.Fatalf("simple contribution = %+v", simple)
	}

	proposal := "<!-- issue-spec:issue=proposal change=external-requirement version=1 -->\n# Proposal\n"
	response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues", jsonBody(map[string]any{
		"title": "standard proposal", "body": proposal, "labels": []string{"ISSUE-SPEC/PROPOSAL"},
	}), "contributor")
	if response.Code != http.StatusCreated {
		t.Fatalf("standard proposal status=%d body=%s", response.Code, response.Body.String())
	}
	var standard codec.Issue
	decode(t, response, &standard)
	if standard.User.Login != contributor.User.Login || len(standard.Labels) != 1 || standard.Labels[0].Name != "issue-spec/proposal" {
		t.Fatalf("standard proposal = %+v", standard)
	}

	standardComments := []string{
		"<!-- issue-spec:type=SPEC id=SPEC-001 version=1 -->\nAgent: Requirements\nType: SPEC\nID: SPEC-001\nStatus: confirmed\nScope: external requirement\nLinks:\n\n## Raw\nThe export MUST omit credentials.\n",
		"<!-- issue-spec:type=QUESTION id=QUESTION-001 version=1 -->\nAgent: Requirements\nType: QUESTION\nID: QUESTION-001\nStatus: open\nScope: external requirement\nLinks:\n\n## Question\nShould the first version exclude attachments?\n",
		"Attachments can remain out of scope for the first version.",
	}
	var discussion codec.Comment
	for index, body := range standardComments {
		response = request(t, mux, http.MethodPost, "/repos/acme/widgets/issues/2/comments",
			jsonBody(map[string]any{"body": body}), "contributor")
		if response.Code != http.StatusCreated {
			t.Fatalf("standard comment %d status=%d body=%s", index, response.Code, response.Body.String())
		}
		var created codec.Comment
		decode(t, response, &created)
		if created.User.Login != contributor.User.Login || created.Body != body {
			t.Fatalf("standard comment %d = %+v", index, created)
		}
		if index == len(standardComments)-1 {
			discussion = created
		}
	}
	response = request(t, mux, http.MethodPatch, fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", discussion.ID),
		jsonBody(map[string]any{"body": "Agreed: attachments remain out of scope."}), "contributor")
	if response.Code != http.StatusOK {
		t.Fatalf("discussion author update status=%d body=%s", response.Code, response.Body.String())
	}
	response = request(t, mux, http.MethodPatch, fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", discussion.ID),
		jsonBody(map[string]any{"body": "other writer"}), "other-contributor")
	if response.Code != http.StatusForbidden {
		t.Fatalf("other discussion update status=%d body=%s", response.Code, response.Body.String())
	}

	deniedCreates := []struct {
		name   string
		body   string
		labels []string
		status int
	}{
		{name: "missing marker", body: "plain", labels: []string{"issue-spec/proposal"}, status: http.StatusForbidden},
		{name: "mismatched marker", body: "<!-- issue-spec:issue=design change=x version=1 -->", labels: []string{"issue-spec/proposal"}, status: http.StatusForbidden},
		{name: "future marker", body: "<!-- issue-spec:issue=proposal change=x version=2 -->", labels: []string{"issue-spec/proposal"}, status: http.StatusForbidden},
		{name: "malformed marker", body: "<!-- issue-spec:issue=proposal change=x version=nope -->", labels: []string{"issue-spec/proposal"}, status: http.StatusForbidden},
		{name: "multiple markers", body: proposal + "<!-- issue-spec:issue=proposal change=other version=1 -->", labels: []string{"issue-spec/proposal"}, status: http.StatusForbidden},
		{name: "additional label", body: proposal, labels: []string{"issue-spec/proposal", "extra"}, status: http.StatusForbidden},
		{name: "duplicate label", body: proposal, labels: []string{"issue-spec/proposal", "ISSUE-SPEC/PROPOSAL"}, status: http.StatusUnprocessableEntity},
	}
	for _, test := range deniedCreates {
		t.Run(test.name, func(t *testing.T) {
			response := request(t, mux, http.MethodPost, "/repos/acme/widgets/issues", jsonBody(map[string]any{
				"title": test.name, "body": test.body, "labels": test.labels,
			}), "contributor")
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}

	response = request(t, mux, http.MethodPatch, "/repos/acme/widgets/issues/1",
		jsonBody(map[string]any{"title": "refined requirement", "body": "refined text"}), "contributor")
	if response.Code != http.StatusOK {
		t.Fatalf("author text update status=%d body=%s", response.Code, response.Body.String())
	}
	var refined codec.Issue
	decode(t, response, &refined)
	if refined.Title != "refined requirement" || refined.Body != "refined text" || refined.State != "open" {
		t.Fatalf("refined issue = %+v", refined)
	}

	deniedUpdates := []struct {
		name       string
		mutation   map[string]any
		credential string
		visibility string
		policy     string
		status     int
	}{
		{name: "author state", mutation: map[string]any{"body": "state attempt", "state": "closed"}, credential: contributor.User.Login, visibility: "public", policy: "public", status: http.StatusForbidden},
		{name: "other author text", mutation: map[string]any{"body": "other attempt"}, credential: other.User.Login, visibility: "public", policy: "public", status: http.StatusForbidden},
		{name: "author under members", mutation: map[string]any{"body": "members attempt"}, credential: contributor.User.Login, visibility: "public", policy: "members", status: http.StatusForbidden},
		{name: "author under disabled", mutation: map[string]any{"body": "disabled attempt"}, credential: contributor.User.Login, visibility: "public", policy: "disabled", status: http.StatusForbidden},
		{name: "author in private repo", mutation: map[string]any{"body": "private attempt"}, credential: contributor.User.Login, visibility: "private", policy: "public", status: http.StatusNotFound},
	}
	for _, test := range deniedUpdates {
		t.Run(test.name, func(t *testing.T) {
			if _, err := environment.pool.Exec(t.Context(), `UPDATE repos SET visibility = $2, contribution_policy = $3 WHERE id = $1`,
				environment.scope.RepoID, test.visibility, test.policy); err != nil {
				t.Fatal(err)
			}
			response := request(t, mux, http.MethodPatch, "/repos/acme/widgets/issues/1", jsonBody(test.mutation), test.credential)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestAuthorNicknameInvalidatesIssueAndCommentRepresentations(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPublic)
	mux := environment.mux(t)
	createdIssue := request(t, mux, http.MethodPost, "/repos/acme/widgets/issues",
		jsonBody(map[string]any{"title": "nickname", "body": "body"}), "owner")
	if createdIssue.Code != http.StatusCreated {
		t.Fatal(createdIssue.Body.String())
	}
	createdComment := request(t, mux, http.MethodPost, "/repos/acme/widgets/issues/1/comments", `{"body":"comment"}`, "owner")
	if createdComment.Code != http.StatusCreated {
		t.Fatal(createdComment.Body.String())
	}
	var comment codec.Comment
	decode(t, createdComment, &comment)

	paths := []string{
		"/repos/acme/widgets/issues?state=all",
		"/repos/acme/widgets/issues/1",
		"/repos/acme/widgets/issues/1/comments",
		fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", comment.ID),
	}
	before := make(map[string]http.Header, len(paths))
	for _, path := range paths {
		response := request(t, mux, http.MethodGet, path, "", "")
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" || response.Header().Get("Last-Modified") == "" {
			t.Fatalf("initial %s status=%d headers=%v body=%s", path, response.Code, response.Header(), response.Body.String())
		}
		before[path] = response.Header().Clone()
	}

	if _, err := environment.pool.Exec(t.Context(), `UPDATE users SET nickname = '澄潭',
		representation_version = representation_version + 1, updated_at = clock_timestamp() + interval '2 seconds'
		WHERE id = $1`, environment.owner.User.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		for header, value := range map[string]string{
			"If-None-Match":     before[path].Get("ETag"),
			"If-Modified-Since": before[path].Get("Last-Modified"),
		} {
			response := request(t, mux, http.MethodGet, path, "", "", map[string]string{header: value})
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "澄潭") {
				t.Fatalf("nickname did not invalidate %s with %s: status=%d headers=%v body=%s",
					path, header, response.Code, response.Header(), response.Body.String())
			}
		}
	}
}

func TestDeletedIssueAuthorIsTextOnlyGhostIdentity(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPublic)
	if _, err := environment.pool.Exec(t.Context(), `INSERT INTO issues
		(id, organization_id, repository_id, number, author_id, title, body)
		VALUES ($1, $2, $3, 1, NULL, 'ghost issue', 'body')`,
		uuid.New(), environment.scope.OrgID, environment.scope.RepoID); err != nil {
		t.Fatal(err)
	}
	response := request(t, environment.mux(t), http.MethodGet, "/repos/acme/widgets/issues/1", "", "")
	var issue codec.Issue
	decode(t, response, &issue)
	if response.Code != http.StatusOK || issue.User.Login != "ghost" || issue.User.HTMLURL != "" || issue.User.AvatarURL != "" {
		t.Fatalf("ghost issue status=%d user=%+v", response.Code, issue.User)
	}
}

func TestIssueCommentConditionalMutationCASAndConflictIdentity(t *testing.T) {
	environment := newEnvironment(t, models.VisibilityPrivate)
	subject := authz.Authenticated(environment.owner)
	_, issue, err := environment.service.CreateIssue(t.Context(), "acme", "widgets", subject,
		models.NewIssue{Title: "conditional", Body: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	_, comment, err := environment.service.CreateComment(t.Context(), "acme", "widgets", issue.Issue.Number, subject, "before")
	if err != nil {
		t.Fatal(err)
	}
	commentID := codec.StableNumericID(comment.Comment.ID.String())
	mux := environment.mux(t)
	path := fmt.Sprintf("/repos/acme/widgets/issues/comments/%d", commentID)

	observed := request(t, mux, http.MethodGet, path, "", "owner")
	if observed.Code != http.StatusOK || observed.Header().Get("X-Issue-Spec-Conditional-Comment-Mutation") != "representation-version" {
		t.Fatalf("observe status=%d headers=%v body=%s", observed.Code, observed.Header(), observed.Body.String())
	}
	version, err := strconv.ParseInt(observed.Header().Get("X-Issue-Spec-Representation-Version"), 10, 64)
	if err != nil || version <= 0 {
		t.Fatalf("observed version=%q err=%v", observed.Header().Get("X-Issue-Spec-Representation-Version"), err)
	}

	success := conditionalCommentRequest(t, mux, path, version, "after")
	if success.Code != http.StatusOK || success.Header().Get("X-Issue-Spec-Representation-Version") != strconv.FormatInt(version+1, 10) {
		t.Fatalf("success status=%d headers=%v body=%s", success.Code, success.Header(), success.Body.String())
	}
	beforeConflictOutbox := countRows(t, environment.pool, "event_outbox")
	stale := conditionalCommentRequest(t, mux, path, version, "stale overwrite")
	if stale.Code != http.StatusConflict ||
		stale.Header().Get("X-Issue-Spec-Representation-Version") != strconv.FormatInt(version+1, 10) ||
		stale.Header().Get("X-Issue-Spec-Expected-Representation-Version") != strconv.FormatInt(version, 10) ||
		stale.Header().Get("X-Issue-Spec-Conditional-Comment-Mutation") != "representation-version" {
		t.Fatalf("stale status=%d headers=%v body=%s", stale.Code, stale.Header(), stale.Body.String())
	}
	if countRows(t, environment.pool, "event_outbox") != beforeConflictOutbox {
		t.Fatal("conflict emitted an outbox event")
	}
	var body string
	var current int64
	if err := environment.pool.QueryRow(t.Context(), `SELECT body, representation_version FROM comments
		WHERE organization_id = $1 AND repository_id = $2 AND compatibility_id = $3`,
		environment.scope.OrgID, environment.scope.RepoID, commentID).Scan(&body, &current); err != nil {
		t.Fatal(err)
	}
	if body != "after" || current != version+1 {
		t.Fatalf("conflict mutated comment body=%q version=%d", body, current)
	}

	invalid := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(`{"body":"invalid"}`))
	invalid.Header.Set("Authorization", "Bearer owner")
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("X-Issue-Spec-Expected-Representation-Version", "not-a-version")
	invalidResponse := httptest.NewRecorder()
	mux.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid precondition status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func conditionalCommentRequest(t *testing.T, handler http.Handler, path string, expected int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(jsonBody(map[string]any{"body": body})))
	request.Header.Set("Authorization", "Bearer owner")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Issue-Spec-Expected-Representation-Version", strconv.FormatInt(expected, 10))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
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

func (e *environment) addOutsider(t *testing.T, login string) serverauth.Principal {
	userID := insertUser(t, e.pool, login)
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

func request(t *testing.T, handler http.Handler, method, path, body, bearer string, headers ...map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Host = "attacker.invalid"
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for _, values := range headers {
		for name, value := range values {
			request.Header.Set(name, value)
		}
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
	return insertPAT(t, pool, owner, "restricted", []string{"issues:write"},
		[]models.RepoScope{{OrgID: orgID, RepoID: repoID}})
}

func insertPAT(t *testing.T, pool *pgxpool.Pool, owner serverauth.Principal, name string,
	scopes []string, repositories []models.RepoScope) serverauth.Principal {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(t.Context(), `INSERT INTO personal_access_tokens
		(id, user_id, name, token_prefix, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, clock_timestamp() + interval '1 hour')`,
		id, owner.User.ID, name, "pat-"+id.String(), []byte(id.String())); err != nil {
		t.Fatal(err)
	}
	for _, scope := range scopes {
		if _, err := pool.Exec(t.Context(), `INSERT INTO pat_scopes
			(id, personal_access_token_id, scope) VALUES ($1, $2, $3)`,
			uuid.New(), id, scope); err != nil {
			t.Fatal(err)
		}
	}
	caps := make([]serverauth.RepositoryCap, 0, len(repositories))
	for _, repository := range repositories {
		if _, err := pool.Exec(t.Context(), `INSERT INTO pat_repositories
			(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`,
			id, repository.OrgID, repository.RepoID); err != nil {
			t.Fatal(err)
		}
		caps = append(caps, serverauth.RepositoryCap{
			OrgID: repository.OrgID, RepoID: repository.RepoID,
		})
	}
	return serverauth.Principal{User: owner.User, Kind: serverauth.CredentialPAT, CredentialID: id,
		Scopes: append([]string(nil), scopes...), RepoRestricted: len(repositories) > 0,
		RepositoryCaps: caps}
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
