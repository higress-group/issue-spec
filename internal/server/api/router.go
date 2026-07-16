// Package api owns the one total HTTP composition for the self-hosted server.
// Feature packages only export RouteSets; this package validates all of them
// together before a listener can become reachable.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	githubcomments "github.com/higress-group/issue-spec/internal/server/api/github/comments"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	githublabels "github.com/higress-group/issue-spec/internal/server/api/github/labels"
	githubpermissions "github.com/higress-group/issue-spec/internal/server/api/github/permissions"
	githubreactions "github.com/higress-group/issue-spec/internal/server/api/github/reactions"
	githubsubscription "github.com/higress-group/issue-spec/internal/server/api/github/subscription"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	nativeauth "github.com/higress-group/issue-spec/internal/server/api/native/auth"
	bindingsapi "github.com/higress-group/issue-spec/internal/server/api/native/bindings"
	boardsapi "github.com/higress-group/issue-spec/internal/server/api/native/boards"
	bootstrapapi "github.com/higress-group/issue-spec/internal/server/api/native/bootstrap"
	contextapi "github.com/higress-group/issue-spec/internal/server/api/native/context"
	delegationapi "github.com/higress-group/issue-spec/internal/server/api/native/delegation"
	deliveriesapi "github.com/higress-group/issue-spec/internal/server/api/native/deliveries"
	evidenceapi "github.com/higress-group/issue-spec/internal/server/api/native/evidence"
	metaapi "github.com/higress-group/issue-spec/internal/server/api/native/meta"
	orgsapi "github.com/higress-group/issue-spec/internal/server/api/native/orgs"
	referencesapi "github.com/higress-group/issue-spec/internal/server/api/native/references"
	reposapi "github.com/higress-group/issue-spec/internal/server/api/native/repos"
	searchapi "github.com/higress-group/issue-spec/internal/server/api/native/search"
	webhooksapi "github.com/higress-group/issue-spec/internal/server/api/native/webhooks"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/auth/takeover"
	"github.com/higress-group/issue-spec/internal/server/bindings"
	"github.com/higress-group/issue-spec/internal/server/changes"
	"github.com/higress-group/issue-spec/internal/server/events/delivery"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/evidence"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	searchservice "github.com/higress-group/issue-spec/internal/server/search"
	"github.com/higress-group/issue-spec/internal/server/spa"
)

type Dependencies struct {
	Admin                *adminservice.Service
	Identity             *serverauth.IdentityService
	Sessions             *session.Service
	PATs                 *pat.Service
	Delegation           *delegation.Service
	Takeover             *takeover.Service
	Authorization        adminservice.Authorizer
	Authentication       serverauth.Middleware
	Adapters             map[string]nativeauth.LoginAdapter
	Avatars              *serverauth.AvatarService
	AuthDiagnostics      nativeauth.DiagnosticObserver
	ServerInstanceID     string
	ProviderDescriptions []codereview.ProviderDescription
	APIOrigin            string
	WebOrigin            string
	TransportPosture     publicurl.TransportPosture

	Issues       *githubissues.Service
	Labels       *githublabels.Service
	Reactions    *githubreactions.Service
	Permissions  *githubpermissions.Service
	Subscription *githubsubscription.Service
	Presenter    codec.Presenter
	Conditional  conditional.Policy

	SPA           *spa.Service
	Bindings      *bindings.Service
	Evidence      *evidence.Service
	Changes       *changes.Service
	Subscriptions *subscriptions.Service
	Deliveries    *delivery.Service
	Search        *searchservice.Service

	DelegationAudience string
	DelegationSubject  string
	Static             http.Handler
	Ready              func(context.Context) error
	LogRequest         func(RequestLog)
}

type RequestLog struct {
	RequestID string
	Method    string
	Status    int
	Duration  time.Duration
}

type metrics struct {
	requests atomic.Uint64
	errors   atomic.Uint64
}

func NewRouter(deps Dependencies) (http.Handler, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	nativeAuthenticate := adminapi.NativeAuthenticate(deps.Authentication)
	nativeAuthenticateOptional := adminapi.NativeAuthenticateOptional(deps.Authentication)
	features := metaapi.Features{Bootstrap: true, PersonalAccessTokens: true, Organizations: true,
		SourceBindings: true, Webhooks: true, ChangeBoards: true, Runner: true, RecoveryExchange: true,
		Search: deps.Search != nil}
	serverMetadata, err := metaapi.NewServerMetadataWithPosture(deps.ServerInstanceID, deps.APIOrigin, deps.WebOrigin, deps.ProviderDescriptions, deps.TransportPosture)
	if err != nil {
		return nil, fmt.Errorf("compose server metadata: %w", err)
	}

	var sets []routeset.RouteSet
	add := func(set routeset.RouteSet, err error) error {
		if err != nil {
			return err
		}
		sets = append(sets, set)
		return nil
	}
	constructors := []func() (routeset.RouteSet, error){
		func() (routeset.RouteSet, error) {
			return githubissues.NewRouteSet(githubissues.Dependencies{Service: deps.Issues, Presenter: deps.Presenter, Authentication: deps.Authentication, Conditional: deps.Conditional})
		},
		func() (routeset.RouteSet, error) {
			return githubcomments.NewRouteSet(githubcomments.Dependencies{Service: deps.Issues, Presenter: deps.Presenter, Authentication: deps.Authentication, Conditional: deps.Conditional})
		},
		func() (routeset.RouteSet, error) {
			return githublabels.NewRouteSet(githublabels.Dependencies{Service: deps.Labels, Presenter: deps.Presenter, Authentication: deps.Authentication, Conditional: deps.Conditional})
		},
		func() (routeset.RouteSet, error) {
			return githubreactions.NewRouteSet(githubreactions.Dependencies{Service: deps.Reactions, Presenter: deps.Presenter, Authentication: deps.Authentication, Conditional: deps.Conditional})
		},
		func() (routeset.RouteSet, error) {
			return githubpermissions.NewRouteSet(githubpermissions.Dependencies{Service: deps.Permissions, Presenter: deps.Presenter, Authentication: deps.Authentication, Conditional: deps.Conditional})
		},
		func() (routeset.RouteSet, error) {
			return githubsubscription.NewRouteSet(githubsubscription.Dependencies{Service: deps.Subscription, Presenter: deps.Presenter, Authentication: deps.Authentication, Conditional: deps.Conditional})
		},
		func() (routeset.RouteSet, error) {
			return bootstrapapi.NewRouteSet(bootstrapapi.Dependencies{Service: deps.Admin})
		},
		func() (routeset.RouteSet, error) {
			return nativeauth.NewRouteSet(nativeauth.Dependencies{Identity: deps.Identity, Sessions: deps.Sessions, PATs: deps.PATs, Authority: deps.Authorization.(nativeauth.IdentityAuthority), Middleware: deps.Authentication, Adapters: deps.Adapters, Avatars: deps.Avatars, Diagnostics: deps.AuthDiagnostics, WebOrigin: deps.WebOrigin})
		},
		func() (routeset.RouteSet, error) {
			return adminapi.NewRouteSet(adminapi.Dependencies{Service: deps.Admin, Authorizer: deps.Authorization, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return orgsapi.NewRouteSet(orgsapi.Dependencies{Service: deps.Admin, Authorizer: deps.Authorization, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return reposapi.NewRouteSet(reposapi.Dependencies{Service: deps.Admin, Authorizer: deps.Authorization, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return contextapi.NewRouteSet(contextapi.Dependencies{Service: deps.SPA, Takeover: deps.Takeover, Sessions: deps.Sessions, Authenticate: nativeAuthenticate, AuthenticateOptional: nativeAuthenticateOptional, AllowedOrigins: deps.Authentication.AllowedOrigins})
		},
		func() (routeset.RouteSet, error) {
			return metaapi.NewRouteSet(metaapi.Dependencies{Features: features, Metadata: serverMetadata})
		},
		func() (routeset.RouteSet, error) {
			return bindingsapi.NewRouteSet(bindingsapi.Dependencies{Service: deps.Bindings, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return referencesapi.NewRouteSet(referencesapi.Dependencies{Service: deps.Bindings, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return evidenceapi.NewRouteSet(evidenceapi.Dependencies{Service: deps.Evidence, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return webhooksapi.NewRouteSet(webhooksapi.Dependencies{Service: deps.Subscriptions, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return deliveriesapi.NewRouteSet(deliveriesapi.Dependencies{Service: deps.Deliveries, Authenticate: nativeAuthenticate})
		},
		func() (routeset.RouteSet, error) {
			return boardsapi.NewRouteSet(boardsapi.Dependencies{Service: deps.Changes, Authenticate: nativeAuthenticate, AuthenticateOptional: nativeAuthenticateOptional})
		},
		func() (routeset.RouteSet, error) {
			return delegationapi.NewRouteSet(delegationapi.Dependencies{Service: deps.Delegation, Authenticate: nativeAuthenticate, Audience: deps.DelegationAudience, Subject: deps.DelegationSubject})
		},
	}
	for _, construct := range constructors {
		if err := add(construct()); err != nil {
			return nil, fmt.Errorf("compose server routes: %w", err)
		}
	}
	if deps.Search != nil {
		if err := add(searchapi.NewRouteSet(searchapi.Dependencies{Service: deps.Search, Authenticate: nativeAuthenticate,
			AuthenticateOptional: nativeAuthenticateOptional, WebOrigin: deps.WebOrigin})); err != nil {
			return nil, fmt.Errorf("compose server routes: %w", err)
		}
	}
	stats := &metrics{}
	if err := add(operationalRoutes(deps.Ready, stats)); err != nil {
		return nil, err
	}
	if err := add(staticRoutes(deps.Static)); err != nil {
		return nil, err
	}
	mux, err := routeset.NewMux(routeset.SelfHostedPolicy(), sets...)
	if err != nil {
		return nil, fmt.Errorf("compose server routes: %w", err)
	}
	handler := securityHeaders(deps.APIOrigin, deps.WebOrigin, mux)
	handler = credentialedCORS(deps.APIOrigin, deps.WebOrigin, handler)
	handler = observeRequests(stats, deps.LogRequest, handler)
	return handler, nil
}

func validateDependencies(deps Dependencies) error {
	if deps.Admin == nil || deps.Identity == nil || deps.Sessions == nil || deps.PATs == nil ||
		deps.Delegation == nil || deps.Takeover == nil || deps.Authorization == nil ||
		deps.Authentication.Sessions == nil || deps.Authentication.Bearer == nil ||
		deps.Issues == nil || deps.Labels == nil || deps.Reactions == nil || deps.Permissions == nil || deps.Subscription == nil ||
		deps.SPA == nil || deps.Bindings == nil || deps.Evidence == nil || deps.Changes == nil ||
		deps.Subscriptions == nil || deps.Deliveries == nil || deps.Static == nil || deps.Ready == nil {
		return errors.New("server router: incomplete dependencies")
	}
	if _, ok := deps.Authorization.(nativeauth.IdentityAuthority); !ok {
		return errors.New("server router: authorization does not provide identity authority")
	}
	if strings.TrimSpace(deps.ServerInstanceID) == "" || strings.TrimSpace(deps.APIOrigin) == "" || strings.TrimSpace(deps.WebOrigin) == "" || !deps.TransportPosture.Valid() || strings.TrimSpace(deps.DelegationAudience) == "" || strings.TrimSpace(deps.DelegationSubject) == "" {
		return errors.New("server router: public origins and delegation bindings are required")
	}
	return nil
}

func operationalRoutes(ready func(context.Context) error, stats *metrics) (routeset.RouteSet, error) {
	plain := func(status int, body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		})
	}
	live := plain(http.StatusOK, "ok\n")
	readyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := ready(r.Context()); err != nil {
			plain(http.StatusServiceUnavailable, "not ready\n").ServeHTTP(w, r)
			return
		}
		plain(http.StatusOK, "ok\n").ServeHTTP(w, r)
	})
	metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "# TYPE issue_spec_http_requests_total counter\nissue_spec_http_requests_total %d\n", stats.requests.Load())
		fmt.Fprintf(w, "# TYPE issue_spec_http_errors_total counter\nissue_spec_http_errors_total %d\n", stats.errors.Load())
	})
	set := routeset.RouteSet{Name: "operations", Routes: []routeset.Route{
		{Name: "operations.live", Method: http.MethodGet, Pattern: "/livez", Handler: live},
		{Name: "operations.ready", Method: http.MethodGet, Pattern: "/readyz", Handler: readyHandler},
		{Name: "operations.metrics", Method: http.MethodGet, Pattern: "/metrics", Handler: metricsHandler},
	}}
	return set, set.Validate()
}

func staticRoutes(handler http.Handler) (routeset.RouteSet, error) {
	// net/http routes HEAD to a matching GET pattern when no more-specific HEAD
	// route exists. A separate HEAD / catch-all would cross-conflict with every
	// more-specific GET API route (method specificity vs path specificity).
	set := routeset.RouteSet{Name: "static-ui", Routes: []routeset.Route{
		{Name: "static.get_and_head", Method: http.MethodGet, Pattern: "/", Handler: handler},
	}}
	return set, set.Validate()
}

func securityHeaders(apiOrigin, webOrigin string, next http.Handler) http.Handler {
	connectSources := "'self'"
	if strings.TrimRight(apiOrigin, "/") != strings.TrimRight(webOrigin, "/") {
		connectSources += " " + strings.TrimRight(apiOrigin, "/")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src "+connectSources)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func credentialedCORS(apiOrigin, webOrigin string, next http.Handler) http.Handler {
	api := strings.TrimRight(strings.TrimSpace(apiOrigin), "/")
	allowed := strings.TrimRight(strings.TrimSpace(webOrigin), "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
		if origin == "" || !apiPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if origin == api {
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if origin != allowed {
			adminapi.WriteProblem(w, http.StatusForbidden, "origin_forbidden", "Origin is not allowed")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", allowed)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, X-CSRF-Token, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func apiPath(value string) bool {
	return value == "/user" || value == "/api" || strings.HasPrefix(value, "/api/") ||
		value == "/repos" || strings.HasPrefix(value, "/repos/")
}

func observeRequests(stats *metrics, log func(RequestLog), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := adminapi.RequestID(r)
		r.Header.Set("X-Request-ID", requestID)
		w.Header().Set("X-Request-ID", requestID)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			stats.requests.Add(1)
			if recorder.status >= 500 {
				stats.errors.Add(1)
			}
			if log != nil {
				log(RequestLog{RequestID: requestID, Method: r.Method, Status: recorder.status, Duration: time.Since(started)})
			}
		}()
		next.ServeHTTP(recorder, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap preserves optional ResponseWriter capabilities through
// http.ResponseController without falsely advertising an interface that the
// underlying writer does not implement. Current server handlers do not use
// direct Flusher/Hijacker/Pusher/ReaderFrom assertions.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
