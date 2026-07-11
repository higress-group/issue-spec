// Package comments exposes GitHub-compatible issue comment routes, including
// the repository-wide recovery list used by runner serve reconciliation.
package comments

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	"github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Dependencies struct {
	Service        *issues.Service
	Presenter      codec.Presenter
	Authentication auth.Middleware
	Conditional    conditional.Policy
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("github comments: service is required")
	}
	authentication := issues.ConfigureCompatibilityAuthentication(deps.Authentication)
	h := handlers{service: deps.Service, presenter: deps.Presenter, conditional: deps.Conditional}
	set := routeset.RouteSet{Name: "github-comments", Routes: []routeset.Route{
		{Name: "github.comments.get_routes", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/issues/{rest...}", Handler: issues.WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.dispatchGet)))},
		{Name: "github.comments.create", Method: http.MethodPost, Pattern: "/repos/{owner}/{repo}/issues/{rest...}", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.dispatchCreate)))},
		{Name: "github.comments.update", Method: http.MethodPatch, Pattern: "/repos/{owner}/{repo}/issues/{rest...}", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.dispatchUpdate)))},
	}}
	return set, set.Validate()
}

type handlers struct {
	service     *issues.Service
	presenter   codec.Presenter
	conditional conditional.Policy
}

func (h handlers) dispatchGet(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.PathValue("rest"), "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "comments":
		h.listRepository(w, r)
	case len(parts) == 2 && parts[0] == "comments":
		r.SetPathValue("comment", parts[1])
		h.get(w, r)
	case len(parts) == 2 && parts[1] == "comments":
		r.SetPathValue("number", parts[0])
		h.listIssue(w, r)
	default:
		apierrors.WriteGitHub(w, apierrors.NotFound(issues.RequestID(r)))
	}
}

func (h handlers) dispatchCreate(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.PathValue("rest"), "/"), "/")
	if len(parts) != 2 || parts[1] != "comments" {
		apierrors.WriteGitHub(w, apierrors.NotFound(issues.RequestID(r)))
		return
	}
	r.SetPathValue("number", parts[0])
	h.create(w, r)
}

func (h handlers) dispatchUpdate(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.PathValue("rest"), "/"), "/")
	if len(parts) != 2 || parts[0] != "comments" {
		apierrors.WriteGitHub(w, apierrors.NotFound(issues.RequestID(r)))
		return
	}
	r.SetPathValue("comment", parts[1])
	h.update(w, r)
}

func (h handlers) listIssue(w http.ResponseWriter, r *http.Request) {
	number, ok := positivePath(w, r, "number")
	if !ok {
		return
	}
	h.list(w, r, &number)
}

func (h handlers) listRepository(w http.ResponseWriter, r *http.Request) { h.list(w, r, nil) }

func (h handlers) list(w http.ResponseWriter, r *http.Request, issueNumber *int64) {
	pageOptions, err := pagination.Parse(r.URL.Query())
	if err != nil {
		var parseError *pagination.ParseError
		if errors.As(err, &parseError) {
			apierrors.WriteGitHub(w, apierrors.PaginationValidation(issues.RequestID(r), parseError))
			return
		}
		issues.WriteError(w, r, err)
		return
	}
	resource, page, err := h.service.ListComments(r.Context(), r.PathValue("owner"), r.PathValue("repo"),
		issues.Subject(r), models.CommentListOptions{IssueNumber: issueNumber, Since: pageOptions.Since,
			Page: pageOptions.Page, PerPage: pageOptions.PerPage})
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("comments", resource.Scope.OrgID, resource.Scope.RepoID,
		page.CollectionVersion, issueNumber, r.URL.Query().Encode(), pageOptions.Page, pageOptions.PerPage)
	if pagination.WriteNotModified(w, r, etag, page.LastModified, h.conditional.Rate()) {
		return
	}
	if link, err := pagination.BuildLinkHeader(h.presenter.Origins.API, r.URL.Path, r.URL.Query(),
		pageOptions.Page, pageOptions.PerPage, page.Total); err == nil && link != "" {
		w.Header().Set("Link", link)
	}
	items := make([]codec.Comment, len(page.Items))
	for index, item := range page.Items {
		items[index] = issues.PresentComment(h.presenter, resource, item)
	}
	issues.WriteJSON(w, http.StatusOK, items)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := positivePath(w, r, "comment")
	if !ok {
		return
	}
	resource, item, err := h.service.GetComment(r.Context(), r.PathValue("owner"), r.PathValue("repo"), id, issues.Subject(r))
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := commentETag(item)
	if pagination.WriteNotModified(w, r, etag, item.Comment.UpdatedAt, h.conditional.Rate()) {
		return
	}
	issues.WriteJSON(w, http.StatusOK, issues.PresentComment(h.presenter, resource, item))
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	number, ok := positivePath(w, r, "number")
	if !ok {
		return
	}
	var input codec.CommentInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		apierrors.WriteGitHub(w, apierrors.Validation(issues.RequestID(r), []codec.Violation{{Resource: "IssueComment", Field: "request", Code: "invalid", Message: "invalid JSON"}}))
		return
	}
	violations := input.Validate()
	if input.Body != nil {
		violations = append(violations, issues.ValidateRawBody("IssueComment", *input.Body)...)
	}
	if len(violations) > 0 {
		apierrors.WriteGitHub(w, apierrors.Validation(issues.RequestID(r), violations))
		return
	}
	resource, item, err := h.service.CreateComment(r.Context(), r.PathValue("owner"), r.PathValue("repo"),
		number, issues.Subject(r), *input.Body)
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setCommentConditional(w, item)
	issues.WriteJSON(w, http.StatusCreated, issues.PresentComment(h.presenter, resource, item))
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := positivePath(w, r, "comment")
	if !ok {
		return
	}
	var input codec.CommentInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		apierrors.WriteGitHub(w, apierrors.Validation(issues.RequestID(r), []codec.Violation{{Resource: "IssueComment", Field: "request", Code: "invalid", Message: "invalid JSON"}}))
		return
	}
	violations := input.Validate()
	if input.Body != nil {
		violations = append(violations, issues.ValidateRawBody("IssueComment", *input.Body)...)
	}
	if len(violations) > 0 {
		apierrors.WriteGitHub(w, apierrors.Validation(issues.RequestID(r), violations))
		return
	}
	resource, item, err := h.service.UpdateComment(r.Context(), r.PathValue("owner"), r.PathValue("repo"),
		id, issues.Subject(r), *input.Body)
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setCommentConditional(w, item)
	issues.WriteJSON(w, http.StatusOK, issues.PresentComment(h.presenter, resource, item))
}

func positivePath(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || value <= 0 {
		apierrors.WriteGitHub(w, apierrors.NotFound(issues.RequestID(r)))
		return 0, false
	}
	return value, true
}

func commentETag(item models.CommentSnapshot) string {
	return pagination.StrongETag("comment", item.Comment.ID, item.Comment.RepresentationVersion,
		item.Comment.ReactionsCollectionVersion)
}

func (h handlers) setCommentConditional(w http.ResponseWriter, item models.CommentSnapshot) {
	pagination.SetConditionalHeaders(w.Header(), commentETag(item), item.Comment.UpdatedAt)
	pagination.SetRateHeaders(w.Header(), h.conditional.Rate())
}
