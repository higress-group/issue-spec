// Package reactions implements GitHub-compatible issue-comment reactions.
package reactions

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
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
		return routeset.RouteSet{}, errors.New("github reactions: service is required")
	}
	authentication := issues.ConfigureCompatibilityAuthentication(deps.Authentication)
	h := handlers{service: deps.Service, presenter: deps.Presenter, conditional: deps.Conditional}
	set := routeset.RouteSet{Name: "github-reactions", Routes: []routeset.Route{
		{Name: "github.reactions.list", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/issues/comments/{comment}/reactions", Handler: issues.WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.list)))},
		{Name: "github.reactions.create", Method: http.MethodPost, Pattern: "/repos/{owner}/{repo}/issues/comments/{comment}/reactions", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.create)))},
		{Name: "github.reactions.delete", Method: http.MethodDelete, Pattern: "/repos/{owner}/{repo}/issues/comments/{comment}/reactions/{reaction}", Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.delete)))},
	}}
	return set, set.Validate()
}

type handlers struct {
	service     *Service
	presenter   codec.Presenter
	conditional conditional.Policy
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	commentID, ok := positive(w, r, "comment")
	if !ok {
		return
	}
	options, err := pagination.Parse(r.URL.Query())
	if err != nil {
		var parsed *pagination.ParseError
		if errors.As(err, &parsed) {
			apierrors.WriteGitHub(w, apierrors.PaginationValidation(issues.RequestID(r), parsed))
			return
		}
		issues.WriteError(w, r, err)
		return
	}
	_, page, err := h.service.List(r.Context(), r.PathValue("owner"), r.PathValue("repo"), commentID, issues.Subject(r), options.Page, options.PerPage)
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("comment-reactions", page.CommentID, page.CollectionVersion, options.Page, options.PerPage)
	if pagination.WriteNotModified(w, r, etag, page.LastModified, h.conditional.Rate()) {
		return
	}
	if link, err := pagination.BuildLinkHeader(h.presenter.Origins.API, r.URL.Path, r.URL.Query(), options.Page, options.PerPage, page.Total); err == nil && link != "" {
		w.Header().Set("Link", link)
	}
	items := make([]codec.Reaction, len(page.Items))
	for index, reaction := range page.Items {
		items[index] = presentReaction(h.presenter, reaction)
	}
	issues.WriteJSON(w, http.StatusOK, items)
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	commentID, ok := positive(w, r, "comment")
	if !ok {
		return
	}
	var input codec.ReactionInput
	if err := codec.DecodeJSON(r.Body, &input); err != nil {
		h.validation(w, r, []codec.Violation{{Resource: "Reaction", Field: "request", Code: "invalid", Message: "invalid JSON"}})
		return
	}
	if violations := input.Validate(); len(violations) > 0 {
		h.validation(w, r, violations)
		return
	}
	_, mutation, err := h.service.Create(r.Context(), r.PathValue("owner"), r.PathValue("repo"), commentID, issues.Subject(r), input.Content)
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setCommentHeaders(w, mutation.Comment)
	status := http.StatusCreated
	if !mutation.Created {
		status = http.StatusOK
	}
	issues.WriteJSON(w, status, presentReaction(h.presenter, mutation.Reaction))
}

func (h handlers) delete(w http.ResponseWriter, r *http.Request) {
	commentID, ok := positive(w, r, "comment")
	if !ok {
		return
	}
	reactionID, ok := positive(w, r, "reaction")
	if !ok {
		return
	}
	_, comment, err := h.service.Delete(r.Context(), r.PathValue("owner"), r.PathValue("repo"), commentID, reactionID, issues.Subject(r))
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	h.setCommentHeaders(w, comment)
	w.WriteHeader(http.StatusNoContent)
}

func presentReaction(p codec.Presenter, reaction models.CommentReaction) codec.Reaction {
	return p.PresentReaction(codec.ReactionView{StableID: reaction.ID.String(),
		Author:  codec.UserView{StableID: stableUserID(reaction.UserID), Login: reaction.AuthorLogin},
		Content: reaction.ReactionKey, CreatedAt: reaction.CreatedAt})
}

func stableUserID(id *uuid.UUID) string {
	if id == nil {
		return "ghost"
	}
	return id.String()
}

func (h handlers) setCommentHeaders(w http.ResponseWriter, comment models.CommentSnapshot) {
	pagination.SetConditionalHeaders(w.Header(), pagination.StrongETag("comment", comment.Comment.ID,
		comment.Comment.RepresentationVersion, comment.Comment.ReactionsCollectionVersion), comment.Comment.UpdatedAt)
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
