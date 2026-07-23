package previews

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type previewCall struct {
	owner, repo, id, digest string
	issue                   int64
	subject                 authz.Subject
	source                  githubissues.PreviewSource
}

type fakePreviewService struct {
	document string
	err      error
	calls    []previewCall
}

func (s *fakePreviewService) PreviewDocument(_ context.Context, owner, repo string, issue int64,
	subject authz.Subject, source githubissues.PreviewSource, id, digest string) (string, error) {
	s.calls = append(s.calls, previewCall{owner: owner, repo: repo, issue: issue,
		subject: subject, source: source, id: id, digest: digest})
	return s.document, s.err
}

func TestPreviewDocumentRoutePassesExactSelectorAndOverridesDocumentHeaders(t *testing.T) {
	service := &fakePreviewService{document: "<!doctype html><script>parent.postMessage('ok','*')</script>"}
	handler := previewMux(t, service)
	digest := strings.Repeat("a", 64)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		"/api/v1/repos/acme/widgets/issues/7/previews/design-review?source=comment&comment_id=42&digest="+digest, nil))
	if response.Code != http.StatusOK || response.Body.String() != service.document || len(service.calls) != 1 {
		t.Fatalf("response=%d/%q calls=%+v", response.Code, response.Body.String(), service.calls)
	}
	call := service.calls[0]
	if call.owner != "acme" || call.repo != "widgets" || call.issue != 7 || call.id != "design-review" ||
		call.digest != digest || call.source.Kind != githubissues.PreviewSourceComment ||
		call.source.CommentID != 42 || call.subject.Principal != nil {
		t.Fatalf("call=%+v", call)
	}
	headers := response.Header()
	if headers.Get("Content-Type") != "text/html; charset=utf-8" ||
		headers.Get("Cache-Control") != "no-store" ||
		headers.Get("Content-Security-Policy") != documentCSP ||
		headers.Get("X-Frame-Options") != "SAMEORIGIN" ||
		headers.Get("X-Content-Type-Options") != "nosniff" ||
		headers.Get("Referrer-Policy") != "no-referrer" ||
		headers.Get("Permissions-Policy") != permissionsPolicy {
		t.Fatalf("headers=%v", headers)
	}
}

func TestPreviewDocumentRouteRejectsMalformedSelectorsBeforeStoredRead(t *testing.T) {
	service := &fakePreviewService{document: "must not be returned"}
	handler := previewMux(t, service)
	for _, target := range []string{
		"/api/v1/repos/o/r/issues/1/previews/x?source=issue",
		"/api/v1/repos/o/r/issues/1/previews/x?source=issue&comment_id=1&digest=" + strings.Repeat("a", 64),
		"/api/v1/repos/o/r/issues/1/previews/x?source=comment&comment_id=0&digest=" + strings.Repeat("a", 64),
		"/api/v1/repos/o/r/issues/1/previews/x?source=other&digest=" + strings.Repeat("a", 64),
		"/api/v1/repos/o/r/issues/1/previews/x?source=issue&digest=" + strings.Repeat("a", 64) + "&extra=1",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
	if len(service.calls) != 0 {
		t.Fatalf("malformed requests reached service: %+v", service.calls)
	}
}

func TestPreviewDocumentRouteFailsClosedWithoutReturningSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "stale", err: githubissues.ErrPreviewDigestMismatch, status: http.StatusConflict, code: "preview_stale"},
		{name: "malformed", err: githubissues.ErrInvalidPreviewRequest, status: http.StatusUnprocessableEntity, code: "invalid_preview_request"},
		{name: "missing", err: store.ErrNotFound, status: http.StatusNotFound, code: "not_found"},
		{name: "hidden", err: &githubissues.DecisionError{Decision: authz.Decision{Visible: false}}, status: http.StatusNotFound, code: "not_found"},
		{name: "failed", err: errors.New("database failed"), status: http.StatusInternalServerError, code: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakePreviewService{document: "TOP SECRET SOURCE", err: test.err}
			response := httptest.NewRecorder()
			previewMux(t, service).ServeHTTP(response, httptest.NewRequest(http.MethodGet,
				"/api/v1/repos/o/r/issues/1/previews/x?source=issue&digest="+strings.Repeat("a", 64), nil))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.code) ||
				strings.Contains(response.Body.String(), service.document) {
				t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func previewMux(t *testing.T, service Service) http.Handler {
	t.Helper()
	optional := adminapi.Authenticate(func(next http.Handler) http.Handler { return next })
	set, err := NewRouteSet(Dependencies{Service: service, AuthenticateOptional: optional})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.SelfHostedPolicy(), set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}
