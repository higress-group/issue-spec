package issues

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	"github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

const requestIDHeader = "X-Request-ID"

func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get(requestIDHeader))
		if requestID == "" || len(requestID) > 128 {
			requestID = uuid.NewString()
		}
		r.Header.Set(requestIDHeader, requestID)
		w.Header().Set(requestIDHeader, requestID)
		next.ServeHTTP(w, r)
	})
}

func RequestID(r *http.Request) string { return r.Header.Get(requestIDHeader) }

func ConfigureCompatibilityAuthentication(middleware auth.Middleware) auth.Middleware {
	middleware.Unauthorized = func(w http.ResponseWriter, r *http.Request) {
		apierrors.WriteGitHub(w, apierrors.Unauthorized(RequestID(r)))
	}
	middleware.Forbidden = func(w http.ResponseWriter, r *http.Request) {
		apierrors.WriteGitHub(w, apierrors.Forbidden(RequestID(r)))
	}
	return middleware
}

func Subject(r *http.Request) authz.Subject {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		return authz.Anonymous()
	}
	return authz.Authenticated(principal)
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if denied, ok := IsDecisionError(err); ok {
		problem, _ := denied.Decision.CompatibilityError(RequestID(r))
		apierrors.WriteGitHub(w, problem)
		return
	}
	switch {
	case errors.Is(err, store.ErrLabelNotFound):
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), []codec.Violation{{Resource: "Issue", Field: "labels", Code: "invalid", Message: "contains an unknown label"}}))
	case errors.Is(err, store.ErrNotFound):
		apierrors.WriteGitHub(w, apierrors.NotFound(RequestID(r)))
	case errors.Is(err, store.ErrVersionConflict), errors.Is(err, store.ErrConflict):
		apierrors.WriteGitHub(w, apierrors.GitHubError{Status: http.StatusConflict,
			RequestID: RequestID(r), Envelope: apierrors.Envelope{Message: "Conflict", DocumentationURL: "https://docs.github.com/rest"}})
	case errors.Is(err, store.ErrInvalidInput):
		apierrors.WriteGitHub(w, apierrors.Validation(RequestID(r), []codec.Violation{{Resource: "Request", Field: "request", Code: "invalid", Message: "is invalid"}}))
	default:
		apierrors.WriteGitHub(w, apierrors.GitHubError{Status: http.StatusInternalServerError,
			RequestID: RequestID(r), Envelope: apierrors.Envelope{Message: "Internal Server Error", DocumentationURL: "https://docs.github.com/rest"}})
	}
}

func ValidateRawBody(resource, body string) []codec.Violation {
	if strings.ContainsRune(body, '\x00') {
		return []codec.Violation{{Resource: resource, Field: "body", Code: "invalid", Message: "must not contain NUL"}}
	}
	return nil
}

func PresentIssue(presenter codec.Presenter, resource models.RepositoryResource, snapshot models.IssueSnapshot) codec.Issue {
	labels := make([]codec.LabelView, len(snapshot.Labels))
	for index, label := range snapshot.Labels {
		labels[index] = codec.LabelView{StableID: label.ID.String(), Owner: resource.Owner,
			Repository: resource.Name, Name: label.Name, Color: label.Color, Description: label.Description}
	}
	return presenter.PresentIssue(codec.IssueView{StableID: snapshot.Issue.ID.String(),
		Owner: resource.Owner, Repository: resource.Name, Number: snapshot.Issue.Number,
		State: string(snapshot.Issue.State), Title: snapshot.Issue.Title, Body: snapshot.Issue.Body,
		Author: codec.UserView{StableID: stableAuthorID(snapshot.Issue.AuthorID), Login: snapshot.AuthorLogin},
		Labels: labels, CommentCount: snapshot.CommentCount, CreatedAt: snapshot.Issue.CreatedAt,
		UpdatedAt: snapshot.Issue.UpdatedAt, ClosedAt: snapshot.Issue.ClosedAt})
}

func PresentComment(presenter codec.Presenter, resource models.RepositoryResource, snapshot models.CommentSnapshot) codec.Comment {
	return presenter.PresentComment(codec.CommentView{StableID: snapshot.Comment.ID.String(), Owner: resource.Owner,
		Repository: resource.Name, IssueNumber: snapshot.IssueNumber, Body: snapshot.Comment.Body,
		Author:    codec.UserView{StableID: stableAuthorID(snapshot.Comment.AuthorID), Login: snapshot.AuthorLogin},
		CreatedAt: snapshot.Comment.CreatedAt, UpdatedAt: snapshot.Comment.UpdatedAt})
}

func stableAuthorID(id *uuid.UUID) string {
	if id == nil {
		return "ghost"
	}
	return id.String()
}

func StableRate() pagination.Rate {
	return pagination.Rate{Limit: 5000, Remaining: 4999, Used: 1,
		Reset: time.Now().UTC().Add(time.Hour), Resource: "core"}
}
