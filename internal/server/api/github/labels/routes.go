// Package labels implements the GitHub-compatible repository and issue-label surface.
package labels

import (
	"errors"
	"net/http"
	"strconv"

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
	Service        *Service
	Presenter      codec.Presenter
	Authentication auth.Middleware
	Conditional    conditional.Policy
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("github labels: service is required")
	}
	authentication := issues.ConfigureCompatibilityAuthentication(deps.Authentication)
	h := handlers{service: deps.Service, presenter: deps.Presenter, conditional: deps.Conditional}
	set := routeset.RouteSet{Name: "github-labels", Routes: []routeset.Route{
		{Name: "github.labels.list", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/labels", Handler: issues.WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.list)))},
		{Name: "github.labels.create", Method: http.MethodPost, Pattern: "/repos/{owner}/{repo}/labels", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.create)))},
		{Name: "github.labels.update", Method: http.MethodPatch, Pattern: "/repos/{owner}/{repo}/labels/{name}", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.update)))},
		{Name: "github.issue_labels.list", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/issues/{number}/labels", Handler: issues.WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.listIssue)))},
		{Name: "github.issue_labels.add", Method: http.MethodPost, Pattern: "/repos/{owner}/{repo}/issues/{number}/labels", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.addIssue)))},
		{Name: "github.issue_labels.replace", Method: http.MethodPut, Pattern: "/repos/{owner}/{repo}/issues/{number}/labels", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.replaceIssue)))},
		{Name: "github.issue_labels.remove", Method: http.MethodDelete, Pattern: "/repos/{owner}/{repo}/issues/{number}/labels/{name}", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.removeIssue)))},
	}}
	return set, set.Validate()
}

type handlers struct {
	service     *Service
	presenter   codec.Presenter
	conditional conditional.Policy
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	options, err := pagination.Parse(r.URL.Query())
	if err != nil {
		h.writePaginationError(w, r, err)
		return
	}
	resource, page, err := h.service.List(r.Context(), r.PathValue("owner"), r.PathValue("repo"), issues.Subject(r), options.Page, options.PerPage)
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("labels", resource.Scope.OrgID, resource.Scope.RepoID, page.CollectionVersion, options.Page, options.PerPage)
	if pagination.WriteNotModified(w, r, etag, page.LastModified, h.conditional.Rate()) {
		return
	}
	if link, err := pagination.BuildLinkHeader(h.presenter.Origins.API, r.URL.Path, r.URL.Query(), options.Page, options.PerPage, page.Total); err == nil && link != "" {
		w.Header().Set("Link", link)
	}
	items := presentLabels(h.presenter, resource, page.Items)
	issues.WriteJSON(w, http.StatusOK, items)
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	var input codec.CreateLabelInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		h.validation(w, r, []codec.Violation{{Resource: "Label", Field: "request", Code: "invalid", Message: "invalid JSON"}})
		return
	}
	if violations := input.Validate(); len(violations) > 0 {
		h.validation(w, r, violations)
		return
	}
	resource, label, err := h.service.Create(r.Context(), r.PathValue("owner"), r.PathValue("repo"), issues.Subject(r),
		models.NewLabel{Name: input.Name, Color: input.Color, Description: input.Description})
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setLabelHeaders(w, label)
	issues.WriteJSON(w, http.StatusCreated, presentLabel(h.presenter, resource, label))
}

type updateInput struct {
	NewName     *string `json:"new_name,omitempty"`
	Color       *string `json:"color,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	var input updateInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil || (input.NewName == nil && input.Color == nil && input.Description == nil) {
		h.validation(w, r, []codec.Violation{{Resource: "Label", Field: "request", Code: "invalid", Message: "invalid JSON or empty update"}})
		return
	}
	var violations []codec.Violation
	if input.NewName != nil {
		violations = append(violations, (codec.CreateLabelInput{Name: *input.NewName, Color: "000000"}).Validate()...)
	}
	if input.Color != nil {
		violations = append(violations, (codec.CreateLabelInput{Name: "label", Color: *input.Color}).Validate()...)
	}
	if input.Description != nil {
		violations = append(violations, (codec.CreateLabelInput{Name: "label", Color: "000000", Description: *input.Description}).Validate()...)
	}
	if len(violations) > 0 {
		h.validation(w, r, violations)
		return
	}
	resource, label, err := h.service.Update(r.Context(), r.PathValue("owner"), r.PathValue("repo"), r.PathValue("name"), issues.Subject(r), func(current models.Label) models.LabelUpdate {
		result := models.LabelUpdate{Name: current.Name, Color: current.Color, Description: current.Description}
		if input.NewName != nil {
			result.Name = *input.NewName
		}
		if input.Color != nil {
			result.Color = *input.Color
		}
		if input.Description != nil {
			result.Description = *input.Description
		}
		return result
	})
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setLabelHeaders(w, label)
	issues.WriteJSON(w, http.StatusOK, presentLabel(h.presenter, resource, label))
}

func (h handlers) listIssue(w http.ResponseWriter, r *http.Request) {
	number, ok := positive(w, r, "number")
	if !ok {
		return
	}
	resource, issue, labels, err := h.service.ListIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), number, issues.Subject(r))
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := issueLabelsETag(issue)
	if pagination.WriteNotModified(w, r, etag, issue.UpdatedAt, h.conditional.Rate()) {
		return
	}
	issues.WriteJSON(w, http.StatusOK, presentLabels(h.presenter, resource, labels))
}

func (h handlers) addIssue(w http.ResponseWriter, r *http.Request)     { h.changeIssue(w, r, false) }
func (h handlers) replaceIssue(w http.ResponseWriter, r *http.Request) { h.changeIssue(w, r, true) }

func (h handlers) changeIssue(w http.ResponseWriter, r *http.Request, replace bool) {
	number, ok := positive(w, r, "number")
	if !ok {
		return
	}
	var input codec.LabelsInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		h.validation(w, r, []codec.Violation{{Resource: "Issue", Field: "request", Code: "invalid", Message: "invalid JSON"}})
		return
	}
	if input.Labels == nil {
		h.validation(w, r, []codec.Violation{{Resource: "Issue", Field: "labels", Code: "missing_field", Message: "is required"}})
		return
	}
	if violations := input.Validate(); len(violations) > 0 {
		h.validation(w, r, violations)
		return
	}
	var resource models.RepositoryResource
	var issue models.Issue
	var labels []models.Label
	var err error
	if replace {
		resource, issue, labels, err = h.service.ReplaceIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), number, issues.Subject(r), input.Labels)
	} else {
		resource, issue, labels, err = h.service.AddToIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), number, issues.Subject(r), input.Labels)
	}
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setIssueLabelHeaders(w, issue)
	issues.WriteJSON(w, http.StatusOK, presentLabels(h.presenter, resource, labels))
}

func (h handlers) removeIssue(w http.ResponseWriter, r *http.Request) {
	number, ok := positive(w, r, "number")
	if !ok {
		return
	}
	resource, issue, labels, err := h.service.RemoveFromIssue(r.Context(), r.PathValue("owner"), r.PathValue("repo"), number, issues.Subject(r), r.PathValue("name"))
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setIssueLabelHeaders(w, issue)
	issues.WriteJSON(w, http.StatusOK, presentLabels(h.presenter, resource, labels))
}

func presentLabels(p codec.Presenter, resource models.RepositoryResource, labels []models.Label) []codec.Label {
	result := make([]codec.Label, len(labels))
	for index, label := range labels {
		result[index] = presentLabel(p, resource, label)
	}
	return result
}

func presentLabel(p codec.Presenter, resource models.RepositoryResource, label models.Label) codec.Label {
	return p.PresentLabel(codec.LabelView{StableID: label.ID.String(), Owner: resource.Owner,
		Repository: resource.Name, Name: label.Name, Color: label.Color, Description: label.Description})
}

func issueLabelsETag(issue models.Issue) string {
	return pagination.StrongETag("issue-labels", issue.ID, issue.LabelsCollectionVersion)
}

func (h handlers) setLabelHeaders(w http.ResponseWriter, label models.Label) {
	pagination.SetConditionalHeaders(w.Header(), pagination.StrongETag("label", label.ID, label.RepresentationVersion), label.UpdatedAt)
	pagination.SetRateHeaders(w.Header(), h.conditional.Rate())
}

func (h handlers) setIssueLabelHeaders(w http.ResponseWriter, issue models.Issue) {
	pagination.SetConditionalHeaders(w.Header(), issueLabelsETag(issue), issue.UpdatedAt)
	pagination.SetRateHeaders(w.Header(), h.conditional.Rate())
}

func positive(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	value, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil || value <= 0 {
		apierrors.WriteGitHub(w, apierrors.NotFound(issues.RequestID(r)))
		return 0, false
	}
	return value, true
}

func (h handlers) validation(w http.ResponseWriter, r *http.Request, violations []codec.Violation) {
	apierrors.WriteGitHub(w, apierrors.Validation(issues.RequestID(r), violations))
}

func (h handlers) writePaginationError(w http.ResponseWriter, r *http.Request, err error) {
	var parsed *pagination.ParseError
	if errors.As(err, &parsed) {
		apierrors.WriteGitHub(w, apierrors.PaginationValidation(issues.RequestID(r), parsed))
		return
	}
	issues.WriteError(w, r, err)
}
