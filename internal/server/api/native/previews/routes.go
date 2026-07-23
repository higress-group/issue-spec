// Package previews serves exact stored html-preview documents through a
// dedicated, route-confined browser security boundary.
package previews

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/store"
)

const (
	documentCSP       = "default-src 'none'; base-uri 'none'; object-src 'none'; frame-ancestors 'self'; form-action 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; media-src 'none'; frame-src 'none'; worker-src 'none'"
	permissionsPolicy = "accelerometer=(), autoplay=(), camera=(), display-capture=(), encrypted-media=(), fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(), screen-wake-lock=(), usb=(), web-share=(), xr-spatial-tracking=()"
)

type Service interface {
	PreviewDocument(context.Context, string, string, int64, authz.Subject,
		githubissues.PreviewSource, string, string) (string, error)
}

type Dependencies struct {
	Service              Service
	AuthenticateOptional adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.AuthenticateOptional == nil {
		return routeset.RouteSet{}, errors.New("native previews: service and optional authentication are required")
	}
	h := handlers{service: deps.Service}
	protected := adminapi.WithRequestID(deps.AuthenticateOptional(http.HandlerFunc(h.document)))
	set := routeset.RouteSet{Name: "native-previews", Routes: []routeset.Route{{
		Name: "native.previews.document", Method: http.MethodGet,
		Pattern: "/api/v1/repos/{owner}/{repo}/issues/{number}/previews/{preview}",
		Handler: protected,
	}}}
	return set, set.Validate()
}

type handlers struct{ service Service }

func (h handlers) document(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Preview not found")
		return
	}
	source, ok := parseSource(r)
	if !ok {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_preview_request", "Preview request is invalid")
		return
	}
	document, err := h.service.PreviewDocument(r.Context(), r.PathValue("owner"), r.PathValue("repo"),
		number, requestSubject(r), source, r.PathValue("preview"), r.URL.Query().Get("digest"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", documentCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", permissionsPolicy)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(document))
}

func parseSource(r *http.Request) (githubissues.PreviewSource, bool) {
	query := r.URL.Query()
	sourceValues, sourceOK := query["source"]
	digestValues, digestOK := query["digest"]
	if !sourceOK || len(sourceValues) != 1 || !digestOK || len(digestValues) != 1 {
		return githubissues.PreviewSource{}, false
	}
	switch sourceValues[0] {
	case githubissues.PreviewSourceIssue:
		if len(query) != 2 {
			return githubissues.PreviewSource{}, false
		}
		return githubissues.PreviewSource{Kind: githubissues.PreviewSourceIssue}, true
	case githubissues.PreviewSourceComment:
		commentValues, ok := query["comment_id"]
		if !ok || len(commentValues) != 1 || len(query) != 3 {
			return githubissues.PreviewSource{}, false
		}
		commentID, err := strconv.ParseInt(commentValues[0], 10, 64)
		if err != nil || commentID <= 0 {
			return githubissues.PreviewSource{}, false
		}
		return githubissues.PreviewSource{Kind: githubissues.PreviewSourceComment, CommentID: commentID}, true
	default:
		return githubissues.PreviewSource{}, false
	}
}

func requestSubject(r *http.Request) authz.Subject {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok {
		return authz.Anonymous()
	}
	return authz.Authenticated(principal)
}

func writeError(w http.ResponseWriter, err error) {
	if denied, ok := githubissues.IsDecisionError(err); ok {
		if !denied.Decision.Visible {
			adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Preview not found")
			return
		}
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Preview not found")
	case errors.Is(err, githubissues.ErrPreviewDigestMismatch):
		adminapi.WriteProblem(w, http.StatusConflict, "preview_stale", "Preview source changed")
	case errors.Is(err, githubissues.ErrInvalidPreviewRequest):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_preview_request", "Preview request is invalid")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Preview request failed")
	}
}
