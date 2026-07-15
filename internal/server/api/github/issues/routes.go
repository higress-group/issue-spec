// Package issues exposes GitHub-compatible issue collection and item routes.
package issues

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	"github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Dependencies struct {
	Service        *Service
	Presenter      codec.Presenter
	Authentication auth.Middleware
	Conditional    conditional.Policy
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("github issues: service is required")
	}
	authentication := ConfigureCompatibilityAuthentication(deps.Authentication)
	h := handlers{service: deps.Service, presenter: deps.Presenter, conditional: deps.Conditional}
	set := routeset.RouteSet{Name: "github-issues", Routes: []routeset.Route{
		{Name: "github.repositories.get", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}", Handler: WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.getRepository)))},
		{Name: "github.issues.list", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/issues", Handler: WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.list)))},
		{Name: "github.issues.create", Method: http.MethodPost, Pattern: "/repos/{owner}/{repo}/issues", Handler: WithRequestID(authentication.Authenticate(http.HandlerFunc(h.create)))},
		{Name: "github.issues.get", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/issues/{number}", Handler: WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.get)))},
		{Name: "github.issues.update", Method: http.MethodPatch, Pattern: "/repos/{owner}/{repo}/issues/{number}", Handler: WithRequestID(authentication.Authenticate(http.HandlerFunc(h.update)))},
	}}
	return set, set.Validate()
}

type handlers struct {
	service     *Service
	presenter   codec.Presenter
	conditional conditional.Policy
}

func (h handlers) getRepository(w http.ResponseWriter, r *http.Request) {
	resource, err := h.service.GetRepository(r.Context(), r.PathValue("owner"), r.PathValue("repo"), Subject(r))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("repository", resource.Scope.OrgID, resource.Scope.RepoID,
		resource.IssuesCollectionVersion, resource.CommentsCollectionVersion, resource.UpdatedAt)
	if pagination.WriteNotModified(w, r, etag, resource.UpdatedAt, h.conditional.Rate()) {
		return
	}
	WriteJSON(w, http.StatusOK, h.presenter.PresentRepository(codec.RepositoryView{
		StableID: resource.Scope.RepoID.String(), OwnerStableID: resource.Scope.OrgID.String(),
		Owner: resource.Owner, Name: resource.Name, Private: resource.Visibility != models.VisibilityPublic,
	}))
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	pageOptions, err := pagination.Parse(r.URL.Query())
	if err != nil {
		var parseError *pagination.ParseError
		if errors.As(err, &parseError) {
			apierrors.WriteGitHub(w, apierrors.PaginationValidation(RequestID(r), parseError))
			return
		}
		WriteError(w, r, err)
		return
	}
	options := models.IssueListOptions{Page: pageOptions.Page, PerPage: pageOptions.PerPage, Since: pageOptions.Since}
	switch state := strings.TrimSpace(r.URL.Query().Get("state")); state {
	case "", "open":
		value := models.IssueStateOpen
		options.State = &value
	case "closed":
		value := models.IssueStateClosed
		options.State = &value
	case "all":
		// A nil state is the explicit all-state store filter.
	default:
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), []codec.Violation{{Resource: "Issue", Field: "state", Code: "invalid", Message: "must be open, closed, or all"}}))
		return
	}
	if raw := r.URL.Query().Get("labels"); raw != "" {
		options.Labels = strings.Split(raw, ",")
	}
	resource, page, err := h.service.ListIssues(r.Context(), r.PathValue("owner"), r.PathValue("repo"), Subject(r), options)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("issues", resource.Scope.OrgID, resource.Scope.RepoID,
		page.CollectionVersion, issueAuthorVersions(page.Items), r.URL.Query().Encode(), pageOptions.Page, pageOptions.PerPage)
	if pagination.WriteNotModified(w, r, etag, page.LastModified, h.conditional.Rate()) {
		return
	}
	if link, err := pagination.BuildLinkHeader(h.presenter.Origins.API, r.URL.Path, r.URL.Query(),
		pageOptions.Page, pageOptions.PerPage, page.Total); err == nil && link != "" {
		w.Header().Set("Link", link)
	}
	items := make([]codec.Issue, len(page.Items))
	for index, item := range page.Items {
		items[index] = PresentIssue(h.presenter, resource, item)
	}
	WriteJSON(w, http.StatusOK, items)
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	var input codec.CreateIssueInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), []codec.Violation{{Resource: "Issue", Field: "request", Code: "invalid", Message: "invalid JSON"}}))
		return
	}
	violations := append(input.Validate(), ValidateRawBody("Issue", input.Body)...)
	if len(violations) > 0 {
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), violations))
		return
	}
	resource, item, err := h.service.CreateIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), Subject(r),
		models.NewIssue{Title: input.Title, Body: input.Body, Labels: input.Labels})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.setIssueConditional(w, item)
	WriteJSON(w, http.StatusCreated, PresentIssue(h.presenter, resource, item))
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	number, ok := parsePositivePathInt(w, r, "number")
	if !ok {
		return
	}
	resource, item, err := h.service.GetIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), number, Subject(r))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	etag := issueETag(item)
	if pagination.WriteNotModified(w, r, etag, issueLastModified(item), h.conditional.Rate()) {
		return
	}
	WriteJSON(w, http.StatusOK, PresentIssue(h.presenter, resource, item))
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	number, ok := parsePositivePathInt(w, r, "number")
	if !ok {
		return
	}
	var input codec.UpdateIssueInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), []codec.Violation{{Resource: "Issue", Field: "request", Code: "invalid", Message: "invalid JSON"}}))
		return
	}
	violations := input.Validate()
	if input.Body != nil {
		violations = append(violations, ValidateRawBody("Issue", *input.Body)...)
	}
	if len(violations) > 0 {
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), violations))
		return
	}
	resource, item, err := h.service.UpdateIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), number, Subject(r),
		func(current models.Issue) (models.IssueUpdate, error) {
			update := models.IssueUpdate{Title: current.Title, Body: current.Body, State: current.State}
			if input.Title != nil {
				update.Title = *input.Title
			}
			if input.Body != nil {
				update.Body = *input.Body
			}
			if input.State != nil {
				update.State = models.IssueState(*input.State)
			}
			return update, nil
		})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.setIssueConditional(w, item)
	WriteJSON(w, http.StatusOK, PresentIssue(h.presenter, resource, item))
}

func parsePositivePathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	value, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || value <= 0 {
		apierrors.WriteGitHub(w, apierrors.NotFound(RequestID(r)))
		return 0, false
	}
	return value, true
}

func issueETag(item models.IssueSnapshot) string {
	return pagination.StrongETag("issue", item.Issue.ID, item.Issue.RepresentationVersion,
		item.Issue.CommentsCollectionVersion, item.Issue.LabelsCollectionVersion,
		item.AuthorRepresentationVersion)
}

func (h handlers) setIssueConditional(w http.ResponseWriter, item models.IssueSnapshot) {
	pagination.SetConditionalHeaders(w.Header(), issueETag(item), issueLastModified(item))
	pagination.SetRateHeaders(w.Header(), h.conditional.Rate())
}

func issueAuthorVersions(items []models.IssueSnapshot) []int64 {
	versions := make([]int64, len(items))
	for index := range items {
		versions[index] = items[index].AuthorRepresentationVersion
	}
	return versions
}

func issueLastModified(item models.IssueSnapshot) time.Time {
	if item.AuthorUpdatedAt.After(item.Issue.UpdatedAt) {
		return item.AuthorUpdatedAt
	}
	return item.Issue.UpdatedAt
}
