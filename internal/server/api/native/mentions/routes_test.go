package mentions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
)

func TestCandidateRouteIsSessionOnlyBoundedAndMinimal(t *testing.T) {
	directory := &fakeDirectory{candidates: []serverstore.MentionCandidate{{Login: "alice", DisplayName: "Alice"}}}
	handler := candidateHandler(t, directory, serverauth.CredentialSession)
	response := requestCandidates(handler, "/api/v1/mentions/candidates?q=ali")
	if response.Code != http.StatusOK || directory.prefix != "ali" || directory.limit != 10 ||
		response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response=%d prefix=%q limit=%d headers=%v body=%s", response.Code, directory.prefix, directory.limit, response.Header(), response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"login":"alice"`, `"display_name":"Alice"`, `"avatar_url":"https://issues.test/api/v1/avatars/alice"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %s: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"email", "membership", "permission", "status", "user_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("candidate response exposed %q: %s", forbidden, body)
		}
	}

	for _, path := range []string{"/api/v1/mentions/candidates", "/api/v1/mentions/candidates?q=", "/api/v1/mentions/candidates?q=a&extra=x"} {
		if got := requestCandidates(handler, path); got.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d body=%s", path, got.Code, got.Body.String())
		}
	}
	pat := candidateHandler(t, directory, serverauth.CredentialPAT)
	if got := requestCandidates(pat, "/api/v1/mentions/candidates?q=a"); got.Code != http.StatusForbidden {
		t.Fatalf("PAT response = %d body=%s", got.Code, got.Body.String())
	}
}

func TestCandidateRouteRateLimitsOneSession(t *testing.T) {
	handler := candidateHandler(t, &fakeDirectory{}, serverauth.CredentialSession)
	for index := 0; index < 60; index++ {
		if got := requestCandidates(handler, "/api/v1/mentions/candidates?q=a"); got.Code != http.StatusOK {
			t.Fatalf("request %d = %d", index+1, got.Code)
		}
	}
	limited := requestCandidates(handler, "/api/v1/mentions/candidates?q=a")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "1" ||
		!strings.Contains(limited.Body.String(), "mention_search_rate_limited") {
		t.Fatalf("rate limit = %d headers=%v body=%s", limited.Code, limited.Header(), limited.Body.String())
	}
}

type fakeDirectory struct {
	candidates []serverstore.MentionCandidate
	err        error
	prefix     string
	limit      int
}

func (f *fakeDirectory) MentionCandidates(_ context.Context, _ uuid.UUID, prefix string, limit int) ([]serverstore.MentionCandidate, error) {
	f.prefix, f.limit = prefix, limit
	return f.candidates, f.err
}

func candidateHandler(t *testing.T, directory Directory, kind serverauth.CredentialKind) http.Handler {
	t.Helper()
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := serverauth.Principal{Kind: kind, User: serverauth.User{ID: uuid.MustParse("11111111-1111-4111-8111-111111111111")}}
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}
	set, err := NewRouteSet(Dependencies{Directory: directory, Authenticate: adminapi.Authenticate(authenticate), WebOrigin: "https://issues.test"})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func requestCandidates(handler http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}
