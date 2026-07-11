package deliveries

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/delivery"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestHandlersValidateForwardAndPresentNativeContract(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("missing dependencies were accepted")
	}
	principal := serverauth.Principal{User: serverauth.User{ID: uuid.New(), Login: "maintainer"},
		Kind: serverauth.CredentialSession, CredentialID: uuid.New()}
	fake := &fakeService{}
	set, err := NewRouteSet(Dependencies{Service: fake, Authenticate: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Routes) != 3 {
		t.Fatalf("routes = %d", len(set.Routes))
	}
	mux, err := routeset.NewMux(routeset.SelfHostedPolicy(), set)
	if err != nil {
		t.Fatal(err)
	}
	orgID, repoID, deliveryID := uuid.New(), uuid.New(), uuid.New()
	base := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/deliveries"

	for _, path := range []string{
		"/api/v1/orgs/not-uuid/repos/" + repoID.String() + "/deliveries",
		"/api/v1/orgs/" + orgID.String() + "/repos/not-uuid/deliveries",
		base + "/not-uuid",
	} {
		response := serve(mux, http.MethodGet, path)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid path %s status=%d", path, response.Code)
		}
	}

	fake.listResult = make([]delivery.Delivery, 0)
	listed := serveWithRequestID(mux, http.MethodGet, base, "request-list")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"deliveries":[]`) ||
		listed.Header().Get("Cache-Control") != "no-store" || listed.Header().Get("X-Request-ID") != "request-list" ||
		fake.scope != (models.RepoScope{OrgID: orgID, RepoID: repoID}) || fake.subject.Principal == nil ||
		fake.subject.Principal.User.ID != principal.User.ID {
		t.Fatalf("list status=%d headers=%v body=%s fake=%+v", listed.Code, listed.Header(), listed.Body.String(), fake)
	}

	fake.getResult = delivery.Detail{Delivery: delivery.Delivery{ID: deliveryID, State: "dead"}, Attempts: make([]delivery.Attempt, 0)}
	got := serve(mux, http.MethodGet, base+"/"+deliveryID.String())
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"attempts":[]`) || fake.id != deliveryID {
		t.Fatalf("get status=%d body=%s id=%s", got.Code, got.Body.String(), fake.id)
	}

	fake.redeliverResult = delivery.Delivery{ID: deliveryID, State: "pending"}
	redelivered := serveWithRequestID(mux, http.MethodPost, base+"/"+deliveryID.String()+"/redeliver", "request-redeliver")
	if redelivered.Code != http.StatusAccepted || !strings.Contains(redelivered.Body.String(), `"state":"pending"`) ||
		fake.actor.UserID != principal.User.ID || fake.actor.RequestID != "request-redeliver" || fake.id != deliveryID {
		t.Fatalf("redeliver status=%d body=%s actor=%+v", redelivered.Code, redelivered.Body.String(), fake.actor)
	}
}

func TestHandlersMapConcealmentAndInternalErrors(t *testing.T) {
	principal := serverauth.Principal{User: serverauth.User{ID: uuid.New()}, Kind: serverauth.CredentialSession}
	orgID, repoID, deliveryID := uuid.New(), uuid.New(), uuid.New()
	path := "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/deliveries/" + deliveryID.String()
	for _, test := range []struct {
		err    error
		status int
	}{
		{delivery.ErrNotFound, http.StatusNotFound},
		{delivery.ErrForbidden, http.StatusForbidden},
		{errors.New("database unavailable"), http.StatusInternalServerError},
	} {
		fake := &fakeService{getErr: test.err}
		set, err := NewRouteSet(Dependencies{Service: fake, Authenticate: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
			})
		}})
		if err != nil {
			t.Fatal(err)
		}
		mux, _ := routeset.NewMux(routeset.SelfHostedPolicy(), set)
		response := serve(mux, http.MethodGet, path)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Request-ID") == "" {
			t.Fatalf("error %v status=%d headers=%v body=%s", test.err, response.Code, response.Header(), response.Body.String())
		}
	}
}

type fakeService struct {
	scope           models.RepoScope
	id              uuid.UUID
	subject         authz.Subject
	actor           delivery.Actor
	listResult      []delivery.Delivery
	listErr         error
	getResult       delivery.Detail
	getErr          error
	redeliverResult delivery.Delivery
	redeliverErr    error
}

func (s *fakeService) List(_ context.Context, subject authz.Subject, scope models.RepoScope) ([]delivery.Delivery, error) {
	s.subject, s.scope = subject, scope
	return s.listResult, s.listErr
}

func (s *fakeService) Get(_ context.Context, subject authz.Subject, scope models.RepoScope, id uuid.UUID) (delivery.Detail, error) {
	s.subject, s.scope, s.id = subject, scope, id
	return s.getResult, s.getErr
}

func (s *fakeService) Redeliver(_ context.Context, actor delivery.Actor, subject authz.Subject,
	scope models.RepoScope, id uuid.UUID) (delivery.Delivery, error) {
	s.actor, s.subject, s.scope, s.id = actor, subject, scope, id
	return s.redeliverResult, s.redeliverErr
}

func serve(handler http.Handler, method, path string) *httptest.ResponseRecorder {
	return serveWithRequestID(handler, method, path, "")
}

func serveWithRequestID(handler http.Handler, method, path, requestID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
