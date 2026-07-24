// Package answers exposes the trusted browser boundary for current QUESTION
// confirmation and append-only canonical ANSWER creation.
package answers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

const maxAnswerIntentBytes = 16 * 1024

type Service interface {
	GetQuestion(context.Context, string, string, int64, authz.Subject, string, string) (
		models.RepositoryResource, githubissues.QuestionAuthority, error)
	CreateAnswer(context.Context, string, string, int64, authz.Subject, string,
		githubissues.AnswerIntent) (models.RepositoryResource, models.CommentSnapshot,
		githubissues.QuestionAuthority, error)
}

type Dependencies struct {
	Service      Service
	Presenter    codec.Presenter
	Authenticate adminapi.Authenticate
	WebOrigin    string
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	origin, err := url.Parse(strings.TrimRight(strings.TrimSpace(deps.WebOrigin), "/"))
	if deps.Service == nil || deps.Authenticate == nil || err != nil || origin.Scheme == "" ||
		origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" ||
		(origin.Scheme != "http" && origin.Scheme != "https") {
		return routeset.RouteSet{}, errors.New("native answers: service, authentication and Web origin are required")
	}
	h := handlers{service: deps.Service, presenter: deps.Presenter, webOrigin: origin.String()}
	protect := func(handler http.Handler) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(handler))
	}
	set := routeset.RouteSet{Name: "native-answers", Routes: []routeset.Route{
		{
			Name: "native.answers.question", Method: http.MethodGet,
			Pattern: "/api/v1/repos/{owner}/{repo}/issues/{number}/questions/{question}",
			Handler: protect(http.HandlerFunc(h.question)),
		},
		{
			Name: "native.answers.create", Method: http.MethodPost,
			Pattern: "/api/v1/repos/{owner}/{repo}/issues/{number}/answers",
			Handler: protect(http.HandlerFunc(h.create)),
		},
	}}
	return set, set.Validate()
}

type handlers struct {
	service   Service
	presenter codec.Presenter
	webOrigin string
}

func (h handlers) question(w http.ResponseWriter, r *http.Request) {
	principal, ok := browserPrincipal(w, r)
	if !ok {
		return
	}
	number, ok := positiveIssueNumber(w, r)
	if !ok {
		return
	}
	_, question, err := h.service.GetQuestion(r.Context(), r.PathValue("owner"), r.PathValue("repo"),
		number, authz.Authenticated(principal), h.webOrigin, r.PathValue("question"))
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, question)
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := browserPrincipal(w, r)
	if !ok {
		return
	}
	number, ok := positiveIssueNumber(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAnswerIntentBytes)
	var request struct {
		QuestionID     string   `json:"question_id"`
		QuestionDigest string   `json:"question_digest"`
		OptionIDs      []string `json:"option_ids"`
		Custom         string   `json:"custom"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid answer intent")
		return
	}
	resource, comment, question, err := h.service.CreateAnswer(r.Context(), r.PathValue("owner"),
		r.PathValue("repo"), number, authz.Authenticated(principal), h.webOrigin,
		githubissues.AnswerIntent{QuestionID: request.QuestionID, QuestionDigest: request.QuestionDigest,
			OptionIDs: request.OptionIDs, Custom: request.Custom})
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"comment":                         githubissues.PresentComment(h.presenter, resource, comment),
		"question":                        question.Snapshot,
		"question_representation_version": question.RepresentationVersion,
		"question_body_digest":            question.BodyDigest,
	})
}

func browserPrincipal(w http.ResponseWriter, r *http.Request) (serverauth.Principal, bool) {
	principal := adminapi.Principal(r)
	if principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return serverauth.Principal{}, false
	}
	if principal.Kind != serverauth.CredentialSession {
		adminapi.WriteProblem(w, http.StatusForbidden, "browser_session_required", "Browser session required")
		return serverauth.Principal{}, false
	}
	return principal, true
}

func positiveIssueNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Question not found")
		return 0, false
	}
	return number, true
}

func writeError(w http.ResponseWriter, err error) {
	if denied, ok := githubissues.IsDecisionError(err); ok {
		if !denied.Decision.Visible {
			adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Question not found")
			return
		}
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Question not found")
	case errors.Is(err, githubissues.ErrInvalidAnswerIntent):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_answer_intent", "Answer intent is invalid")
	case errors.Is(err, githubissues.ErrInvalidQuestionAuthority):
		adminapi.WriteProblem(w, http.StatusConflict, "question_invalid", "Current QUESTION cannot accept answers")
	case errors.Is(err, githubissues.ErrQuestionChanged):
		adminapi.WriteProblem(w, http.StatusConflict, "question_changed", "QUESTION changed after confirmation")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Answer request failed")
	}
}
