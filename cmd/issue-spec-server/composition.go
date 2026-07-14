package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/server/admin"
	serverapi "github.com/higress-group/issue-spec/internal/server/api"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	githubissues "github.com/higress-group/issue-spec/internal/server/api/github/issues"
	githublabels "github.com/higress-group/issue-spec/internal/server/api/github/labels"
	githubpermissions "github.com/higress-group/issue-spec/internal/server/api/github/permissions"
	githubreactions "github.com/higress-group/issue-spec/internal/server/api/github/reactions"
	githubsubscription "github.com/higress-group/issue-spec/internal/server/api/github/subscription"
	nativeauth "github.com/higress-group/issue-spec/internal/server/api/native/auth"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/recovery"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/auth/takeover"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/bindings"
	"github.com/higress-group/issue-spec/internal/server/changes"
	"github.com/higress-group/issue-spec/internal/server/config"
	"github.com/higress-group/issue-spec/internal/server/events/delivery"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/evidence"
	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	"github.com/higress-group/issue-spec/internal/server/spa"
	"github.com/higress-group/issue-spec/internal/server/staticui"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type readiness struct {
	accepting atomic.Bool
	worker    atomic.Bool
	database  *store.Store
	timeout   time.Duration
}

func (r *readiness) check(ctx context.Context) error {
	if !r.accepting.Load() || !r.worker.Load() {
		return errors.New("server is draining")
	}
	checkCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := r.database.Ping(checkCtx); err != nil {
		return err
	}
	return r.database.ValidateMigrations(checkCtx)
}

type application struct {
	handler  http.Handler
	database *store.Store
	delivery *delivery.Service
	ready    *readiness
}

func compose(ctx context.Context, cfg config.Config) (*application, error) {
	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	fail := func(err error) (*application, error) {
		database.Close()
		return nil, err
	}
	if err := prepareMigrations(ctx, database, cfg.MigrationsMode); err != nil {
		return fail(err)
	}
	serverInstanceID, err := database.ServerInstanceID(ctx)
	if err != nil {
		return fail(err)
	}
	origins, err := configuredOrigins(cfg)
	if err != nil {
		return fail(err)
	}
	secrets, err := serverauth.NewSecrets(cfg.TokenPepper.Bytes(), cfg.EncryptionKey.Bytes())
	if err != nil {
		return fail(fmt.Errorf("initialize secrets: %w", err))
	}
	authorization, err := authz.New(database.Pool())
	if err != nil {
		return fail(err)
	}
	adminService, err := admin.New(database.Pool(), cfg.BootstrapSecret.Bytes(), secrets)
	if err != nil {
		return fail(err)
	}
	sessions, err := session.New(database.Pool(), secrets, session.Config{Secure: origins.Posture.SecureCookies()})
	if err != nil {
		return fail(err)
	}
	pats := pat.New(database.Pool(), secrets)
	delegated := delegation.New(database.Pool(), secrets)
	recoveryService := recovery.New(database.Pool(), secrets)
	takeoverService, err := takeover.New(database.Pool(), recoveryService, sessions)
	if err != nil {
		return fail(err)
	}
	identity := serverauth.NewIdentityService(database.Pool())
	adapters, err := configureAdapters(ctx, database.Pool(), secrets, origins, cfg.AuthProviders.Bytes(),
		cfg.Environment == config.EnvironmentProduction)
	if err != nil {
		return fail(err)
	}
	avatarOrigins, err := configuredAvatarOrigins(cfg.AuthProviders.Bytes())
	if err != nil {
		return fail(fmt.Errorf("configure avatar origins: %w", err))
	}
	avatars, err := serverauth.NewAvatarService(database.Pool(), serverauth.AvatarConfig{ProviderOrigins: avatarOrigins})
	if err != nil {
		return fail(err)
	}
	providerRegistry, err := codereview.LoadOperatorRegistryFromEnvironment()
	if err != nil {
		return fail(fmt.Errorf("configure code providers: %w", err))
	}
	allowedOrigins := map[string]struct{}{origins.Web.String(): {}}
	authentication := serverauth.Middleware{SessionCookieName: sessions.CookieName(), AllowedOrigins: allowedOrigins,
		Sessions: sessions, Bearer: serverauth.BearerChain{delegated, pats}}

	issueService, err := githubissues.NewService(database, authorization, artifacts.MarkerProjector{}, outbox.Hook{})
	if err != nil {
		return fail(err)
	}
	labelService, err := githublabels.NewService(database, authorization)
	if err != nil {
		return fail(err)
	}
	reactionService, err := githubreactions.NewService(database, authorization)
	if err != nil {
		return fail(err)
	}
	permissionService, err := githubpermissions.NewService(database, authorization)
	if err != nil {
		return fail(err)
	}
	subscriptionCompat, err := githubsubscription.NewService(database, authorization)
	if err != nil {
		return fail(err)
	}
	bindingService, err := bindings.New(database.Pool(), authorization)
	if err != nil {
		return fail(err)
	}
	evidenceService, err := evidence.New(database.Pool(), authorization)
	if err != nil {
		return fail(err)
	}
	changeService, err := changes.New(database.Pool(), authorization, providerRegistry.Descriptions()...)
	if err != nil {
		return fail(err)
	}
	spaService, err := spa.New(database, authorization, origins)
	if err != nil {
		return fail(err)
	}

	policy := networkpolicy.Policy{Production: cfg.Environment == config.EnvironmentProduction,
		AllowHTTP:      cfg.TransportPosture == config.TransportTrustedInternalHTTP,
		AllowedPrivate: append([]netip.Prefix(nil), cfg.WebhookAllowedPrivate...)}
	resolver := net.DefaultResolver
	deliveryClient, err := networkpolicy.NewClient(networkpolicy.Config{Policy: policy, Resolver: resolver})
	if err != nil {
		return fail(err)
	}
	currentWebhookKey, webhookKeySet, err := webhookKeys(cfg.WebhookKeys.Bytes(), cfg.EncryptionKey.Bytes())
	if err != nil {
		return fail(err)
	}
	keyring, err := subscriptions.NewKeyring(currentWebhookKey, webhookKeySet)
	if err != nil {
		return fail(err)
	}
	subscriptionService, err := subscriptions.New(database, authorization, keyring, subscriptions.Config{
		Production:           cfg.Environment == config.EnvironmentProduction,
		DestinationPreflight: networkpolicy.Preflight{Policy: policy, Resolver: resolver},
	})
	if err != nil {
		return fail(err)
	}
	deliveryService, err := delivery.New(database.Pool(), authorization, subscriptionService, deliveryClient, delivery.Config{
		LeaseDuration: cfg.DeliveryLeaseDuration, MaxConcurrency: cfg.DeliveryConcurrency, PollInterval: cfg.DeliveryPollInterval,
	})
	if err != nil {
		return fail(err)
	}
	static, err := staticui.New(staticui.Options{DevelopmentDirectory: cfg.StaticDirectory,
		Production: cfg.Environment == config.EnvironmentProduction})
	if err != nil {
		return fail(err)
	}
	ready := &readiness{database: database, timeout: cfg.HealthReadTimeout}
	ready.accepting.Store(true)
	ready.worker.Store(true)
	handler, err := serverapi.NewRouter(serverapi.Dependencies{
		Admin: adminService, Identity: identity, Sessions: sessions, PATs: pats, Delegation: delegated,
		Takeover: takeoverService, Authorization: authorization, Authentication: authentication,
		Adapters: adapters, Avatars: avatars, AuthDiagnostics: nativeauth.DiagnosticObserverFunc(logAuthenticationDiagnostic),
		ServerInstanceID: serverInstanceID, ProviderDescriptions: providerRegistry.Descriptions(),
		APIOrigin: origins.API.String(), WebOrigin: origins.Web.String(), TransportPosture: origins.Posture,
		Issues: issueService, Labels: labelService, Reactions: reactionService, Permissions: permissionService,
		Subscription: subscriptionCompat, Presenter: codec.Presenter{Origins: origins}, Conditional: conditional.Policy{},
		SPA: spaService, Bindings: bindingService, Evidence: evidenceService, Changes: changeService,
		Subscriptions: subscriptionService, Deliveries: deliveryService,
		DelegationAudience: cfg.DelegationAudience, DelegationSubject: cfg.DelegationSubject,
		Static: static, Ready: ready.check, LogRequest: logRequest,
	})
	if err != nil {
		return fail(err)
	}
	return &application{handler: handler, database: database, delivery: deliveryService, ready: ready}, nil
}

func prepareMigrations(ctx context.Context, database *store.Store, mode config.MigrationsMode) error {
	switch mode {
	case config.MigrationsAuto:
		if err := database.Migrate(ctx); err != nil {
			return fmt.Errorf("migrate storage: %w", err)
		}
	case config.MigrationsValidate:
		if err := database.ValidateMigrations(ctx); err != nil {
			return fmt.Errorf("validate storage migrations: %w", err)
		}
	case config.MigrationsOff:
		// Readiness still validates the exact embedded schema before traffic.
	default:
		return fmt.Errorf("unsupported migration mode %q", mode)
	}
	return nil
}

func configuredOrigins(cfg config.Config) (publicurl.Origins, error) {
	apiURL, webURL := cfg.APIPublicURL, cfg.WebPublicURL
	if cfg.Environment != config.EnvironmentProduction {
		fallback, err := localOrigin(cfg.ListenAddr)
		if err != nil {
			return publicurl.Origins{}, err
		}
		if apiURL == "" {
			apiURL = fallback
		}
		if webURL == "" {
			webURL = fallback
		}
	}
	posture := publicurl.TransportHTTPS
	if cfg.TransportPosture == config.TransportTrustedInternalHTTP {
		posture = publicurl.TransportTrustedInternalHTTP
	}
	if cfg.Environment != config.EnvironmentProduction {
		parsed, _ := url.Parse(apiURL)
		if parsed.Scheme == "http" {
			posture = publicurl.TransportTrustedInternalHTTP
		}
	}
	return publicurl.NewWithPosture(strings.TrimRight(apiURL, "/"), strings.TrimRight(webURL, "/"), cfg.TrustedProxies, posture)
}

func localOrigin(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
}

func logRequest(entry serverapi.RequestLog) {
	payload := map[string]any{"level": "info", "event": "http_request", "request_id": entry.RequestID,
		"method": entry.Method, "status": entry.Status, "duration_ms": entry.Duration.Milliseconds()}
	encoded, _ := json.Marshal(payload)
	fmt.Fprintln(os.Stderr, string(encoded))
}

func logAuthenticationDiagnostic(_ context.Context, diagnostic nativeauth.AuthenticationDiagnostic) {
	payload := map[string]any{"level": "warn", "event": "authentication_diagnostic",
		"request_id": diagnostic.RequestID, "provider": diagnostic.Provider, "reason_code": diagnostic.ReasonCode}
	encoded, _ := json.Marshal(payload)
	fmt.Fprintln(os.Stderr, string(encoded))
}

func run(ctx context.Context, cfg config.Config) error {
	app, err := compose(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.database.Close()
	origins, err := configuredOrigins(cfg)
	if err != nil {
		return err
	}
	startup, _ := json.Marshal(map[string]any{"level": "info", "event": "server_starting",
		"transport_posture": origins.Posture, "api_public_url": origins.API.String(), "web_public_url": origins.Web.String()})
	fmt.Fprintln(os.Stderr, string(startup))
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerDone := make(chan error, 1)
	go func() {
		err := app.delivery.Run(workerCtx)
		app.ready.worker.Store(false)
		workerDone <- err
	}()
	server := &http.Server{Addr: cfg.ListenAddr, Handler: app.handler, ReadTimeout: cfg.HealthReadTimeout,
		ReadHeaderTimeout: cfg.HealthReadTimeout, WriteTimeout: cfg.HealthWriteTimeout, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 1 << 20}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ListenAndServe() }()

	var cause error
	workerConsumed := false
	serveConsumed := false
	select {
	case err := <-serveDone:
		serveConsumed = true
		if !errors.Is(err, http.ErrServerClosed) {
			cause = fmt.Errorf("serve: %w", err)
		}
	case err := <-workerDone:
		workerConsumed = true
		if err == nil {
			err = errors.New("delivery worker stopped unexpectedly")
		}
		cause = fmt.Errorf("delivery worker: %w", err)
	case <-ctx.Done():
	}

	app.ready.accepting.Store(false)
	app.delivery.StopClaims()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.GracefulShutdownTimeout)
	defer cancelShutdown()
	shutdownErr := server.Shutdown(shutdownCtx)
	var workerErr error
	if !workerConsumed {
		workerErr = waitWorker(shutdownCtx, workerDone, cancelWorker)
	}
	if shutdownErr != nil && cause == nil {
		cause = fmt.Errorf("graceful HTTP shutdown: %w", shutdownErr)
	}
	if workerErr != nil && cause == nil {
		cause = fmt.Errorf("delivery worker shutdown: %w", workerErr)
	}
	if !serveConsumed {
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && cause == nil {
				cause = fmt.Errorf("serve during shutdown: %w", err)
			}
		case <-shutdownCtx.Done():
			if cause == nil {
				cause = fmt.Errorf("HTTP server did not stop: %w", shutdownCtx.Err())
			}
		}
	}
	return cause
}

func waitWorker(ctx context.Context, done <-chan error, cancel context.CancelFunc) error {
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		cancel()
		select {
		case <-done:
			return ctx.Err()
		case <-time.After(time.Second):
			return errors.New("worker did not release after cancellation")
		}
	}
}
